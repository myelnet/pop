package utils

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/filecoin-project/go-hamt-ipld/v3"
	keystore "github.com/ipfs/go-ipfs-keystore"
	ci "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/host"
	"github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/rs/zerolog/log"
)

// Dumping a bunch of shared stuff here. We can reorganize once we have a clearer idea
// of what all the different components look like

// KLibp2pHost is the datastore key for storing our libp2p identity private key
const KLibp2pHost = "libp2p-host"

// RepoPath is akin to IPFS: ~/.pop by default or changed via $POP_PATH
func RepoPath() string {
	if path, ok := os.LookupEnv("POP_PATH"); ok {
		return path
	}
	return ".pop"
}

// FullPath constructs full path and check if a repo was initialized with a datastore
func FullPath(path string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, path), nil
}

// RepoExists checks if we have a datastore directory already created
func RepoExists(path string) (bool, error) {
	_, err := os.Stat(filepath.Join(path, "datastore"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Libp2pKey gets a libp2p host private key from the keystore if available or generates a new one
func Libp2pKey(ks keystore.Keystore) (ci.PrivKey, error) {
	k, err := ks.Get(KLibp2pHost)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, keystore.ErrNoSuchKey) {
		return nil, err
	}
	pk, _, err := ci.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := ks.Put(KLibp2pHost, pk); err != nil {
		return nil, err
	}
	return pk, nil
}

// Bootstrap connects to a list of provided peer addresses, libp2p then uses dht discovery
// to connect with all the peers the node is aware of
func Bootstrap(ctx context.Context, h host.Host, bpeers []string) error {
	var peers []peer.AddrInfo
	for _, addrStr := range bpeers {
		addrInfo, err := AddrStringToAddrInfo(addrStr)
		if err != nil {
			continue
		}
		peers = append(peers, *addrInfo)
	}

	var wg sync.WaitGroup
	peerInfos := make(map[peer.ID]*peer.AddrInfo, len(peers))
	for _, pii := range peers {
		pi, ok := peerInfos[pii.ID]
		if !ok {
			pi = &peer.AddrInfo{ID: pii.ID}
			peerInfos[pi.ID] = pi
		}
		pi.Addrs = append(pi.Addrs, pii.Addrs...)
	}

	wg.Add(len(peerInfos))
	for _, peerInfo := range peerInfos {
		go func(peerInfo *peer.AddrInfo) {
			defer wg.Done()
			err := h.Connect(ctx, *peerInfo)
			if err != nil {
				log.Error().Err(err).Str("peerId", peerInfo.ID.String()).Msg("failed to connect to peer")
			} else {
				log.Trace().Str("peerId", peerInfo.ID.String()).Msg("successfully connected to peer")
			}
		}(peerInfo)
	}
	wg.Wait()
	return nil
}

// FormatToken takes a token type and a token value and creates a string ready to
// send in the http Authorization header
func FormatToken(tok string, tp string) string {
	var token string
	if tok == "" {
		return token
	}
	// Basic auth requires base64 encoding and Infura api provides unencoded strings
	if tp == "Basic" {
		token = base64.StdEncoding.EncodeToString([]byte(tok))
	} else {
		token = tok
	}
	token = fmt.Sprintf("%s %s", tp, token)
	return token
}

type listValue struct{ val *[]string }

func ListValue(lst *[]string, defaultList []string) flag.Value {
	*lst = defaultList
	return listValue{lst}
}

func (l listValue) String() string {
	if l.val != nil {
		return strings.Join(*l.val, ",")
	}
	return ""
}

func (l listValue) Set(v string) error {
	if v == "" {
		return errors.New("set an empty string")
	}
	*l.val = strings.Split(v, ",")
	return nil
}

// AddrStringToAddrInfo turns a string address of format /p2p/<addr>/<peerid> to AddrInfo struct
func AddrStringToAddrInfo(s string) (*peer.AddrInfo, error) {
	addr, err := ma.NewMultiaddr(s)
	if err != nil {
		return nil, err
	}
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, err
	}

	return addrInfo, nil
}

// AddrBytesToAddrInfo tunrs a compressed address into addrInfo struct
func AddrBytesToAddrInfo(b []byte) (*peer.AddrInfo, error) {
	addr, err := ma.NewMultiaddrBytes(b)
	if err != nil {
		return nil, err
	}
	addrInfo, err := peer.AddrInfoFromP2pAddr(addr)
	if err != nil {
		return nil, err
	}
	return addrInfo, nil
}

// HAMTHashOption uses 256 hash to prevent collision attacks
var HAMTHashOption = hamt.UseHashFunction(func(input []byte) []byte {
	res := sha256.Sum256(input)
	return res[:]
})
