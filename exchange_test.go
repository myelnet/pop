package hop

import (
	"context"
	"fmt"
	"testing"
	"time"

	cidlink "github.com/ipld/go-ipld-prime/linking/cid"
	peer "github.com/libp2p/go-libp2p-core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/myelnet/go-hop-exchange/supply"
	"github.com/myelnet/go-hop-exchange/testutil"
	"github.com/myelnet/go-hop-exchange/wallet"
	"github.com/stretchr/testify/require"
)

func TestExchangeDirect(t *testing.T) {
	// Iterating a ton helps weed out false positives
	for i := 0; i < 1; i++ {
		t.Run(fmt.Sprintf("Try %v", i), func(t *testing.T) {
			bgCtx := context.Background()

			ctx, cancel := context.WithTimeout(bgCtx, 10*time.Second)
			defer cancel()

			mn := mocknet.New(bgCtx)

			var client *Exchange
			var cnode *testutil.TestNode

			providers := make(map[peer.ID]*Exchange)
			pnodes := make(map[peer.ID]*testutil.TestNode)

			for i := 0; i < 11; i++ {
				n := testutil.NewTestNode(mn, t)
				n.SetupGraphSync(ctx)
				n.SetupTempRepo(t)
				ps, err := pubsub.NewGossipSub(ctx, n.Host)
				require.NoError(t, err)

				settings := Settings{
					Datastore:  n.Ds,
					Blockstore: n.Bs,
					Host:       n.Host,
					PubSub:     ps,
					GraphSync:  n.Gs,
					RepoPath:   n.DTTmpDir,
					Keystore:   wallet.NewMemKeystore(),
				}

				exch, err := NewExchange(bgCtx, settings)
				require.NoError(t, err)

				if i == 0 {
					client = exch
					cnode = n
				} else {
					providers[n.Host.ID()] = exch
					pnodes[n.Host.ID()] = n
				}
			}

			require.NoError(t, mn.LinkAll())

			require.NoError(t, mn.ConnectAllButSelf())

			link, origBytes := cnode.LoadUnixFSFileToStore(ctx, t, "/README.md")
			rootCid := link.(cidlink.Link).Cid

			// In tis test we expect the maximum of providers to receive the content
			// that may not be the case in the real world
			receivers := make(chan peer.ID, 6)
			done := make(chan error)
			client.Supply().SubscribeToEvents(func(event supply.Event) {
				require.Equal(t, rootCid, event.PayloadCID)
				receivers <- event.Provider
				if len(receivers)+1 == cap(receivers) {
					done <- nil
				}
			})

			err := client.Dispatch(rootCid)
			require.NoError(t, err)

			select {
			case <-ctx.Done():
				t.Fatal("couldn't finish content propagation")
			case <-done:
			}

			// Gather and check all the recipients have a proper copy of the file
			pp, err := client.Supply().ProviderPeersForContent(rootCid)
			require.NoError(t, err)
			for _, p := range pp {
				pnodes[p].VerifyFileTransferred(ctx, t, rootCid, origBytes)
			}

			cnode.NukeBlockstore(ctx, t)

			// Sanity check to make sure our client does not have a copy of our blocks
			_, err = cnode.DAG.Get(ctx, rootCid)
			require.Error(t, err)

			// Now we fetch it again from our providers
			session, err := client.NewSession(ctx, rootCid)
			require.NoError(t, err)
			defer session.Close()

			offer, err := session.QueryGossip(ctx)
			require.NoError(t, err)

			err = session.SyncBlocks(ctx, offer)
			require.NoError(t, err)

			select {
			case err := <-session.Done():
				require.NoError(t, err)
			case <-ctx.Done():
				t.Fatal("failed to finish sync")
			}

			// And we verify we got the file back
			cnode.VerifyFileTransferred(ctx, t, rootCid, origBytes)
		})
	}
}
