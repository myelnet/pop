package storage

import (
	"bytes"
	"context"
	"fmt"

	cborutil "github.com/filecoin-project/go-cbor-util"
	miner3 "github.com/filecoin-project/specs-actors/v3/actors/builtin"
	market3 "github.com/filecoin-project/specs-actors/v3/actors/builtin/market"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-fil-markets/shared"
	sm "github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/filecoin-project/go-state-types/big"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/filecoin-project/go-state-types/exitcode"
	"github.com/ipfs/go-cid"
	peer "github.com/libp2p/go-libp2p-peer"
	"github.com/multiformats/go-multiaddr"
	fil "github.com/myelnet/go-hop-exchange/filecoin"
	"github.com/myelnet/go-hop-exchange/wallet"
)

// Adapter implements the interface required by the Filecoin storage market client
type Adapter struct {
	fAPI    fil.API
	wallet  wallet.Driver
	fundmgr *FundManager
}

// GetChainHead returns a tipset token for the current chain head
func (a *Adapter) GetChainHead(ctx context.Context) (shared.TipSetToken, abi.ChainEpoch, error) {
	head, err := a.fAPI.ChainHead(ctx)
	if err != nil {
		return nil, 0, err
	}

	return head.Key().Bytes(), head.Height(), nil
}

// AddFunds with the StorageMinerActor for a storage participant.  Used by both providers and clients.
func (a *Adapter) AddFunds(ctx context.Context, addr address.Address, amount abi.TokenAmount) (cid.Cid, error) {
	var err error
	msg := &fil.Message{
		To:     miner3.StorageMarketActorAddr,
		From:   addr,
		Value:  amount,
		Method: miner3.MethodsMarket.AddBalance,
	}
	msg, err = a.fAPI.GasEstimateMessageGas(ctx, msg, nil, fil.EmptyTSK)
	if err != nil {
		return cid.Undef, err
	}
	act, err := a.fAPI.StateGetActor(ctx, msg.From, fil.EmptyTSK)
	if err != nil {
		return cid.Undef, err
	}
	msg.Nonce = act.Nonce
	mbl, err := msg.ToStorageBlock()
	if err != nil {
		return cid.Undef, err
	}
	sig, err := a.wallet.Sign(ctx, msg.From, mbl.Cid().Bytes())
	if err != nil {
		return cid.Undef, err
	}

	smsg := &fil.SignedMessage{
		Message:   *msg,
		Signature: *sig,
	}
	return a.fAPI.MpoolPush(ctx, smsg)
}

// ReserveFunds reserves the given amount of funds is ensures it is available for the deal
func (a *Adapter) ReserveFunds(ctx context.Context, wallet, addr address.Address, amt abi.TokenAmount) (cid.Cid, error) {
	return a.fundmgr.Reserve(ctx, wallet, addr, amt)
}

// ReleaseFunds releases funds reserved with ReserveFunds
func (a *Adapter) ReleaseFunds(ctx context.Context, addr address.Address, amt abi.TokenAmount) error {
	return a.fundmgr.Release(addr, amt)
}

// GetBalance returns locked/unlocked for a storage participant.  Used by both providers and clients.
func (a *Adapter) GetBalance(ctx context.Context, addr address.Address, tok shared.TipSetToken) (sm.Balance, error) {
	tsk, err := fil.TipSetKeyFromBytes(tok)
	if err != nil {
		return sm.Balance{}, err
	}

	bal, err := a.fAPI.StateMarketBalance(ctx, addr, tsk)
	if err != nil {
		return sm.Balance{}, err
	}

	return sm.Balance{
		Locked:    bal.Locked,
		Available: big.Sub(bal.Escrow, bal.Locked),
	}, nil
}

// VerifySignature verifies a given set of data was signed properly by a given address's private key
func (a *Adapter) VerifySignature(ctx context.Context, sig crypto.Signature, signer address.Address, plaintext []byte, tok shared.TipSetToken) (bool, error) {
	addr, err := a.fAPI.StateAccountKey(ctx, signer, fil.EmptyTSK)
	if err != nil {
		return false, err
	}

	return a.wallet.Verify(ctx, addr, plaintext, &sig)
}

// WaitForMessage waits until a message appears on chain. If it is already on chain, the callback is called immediately
func (a *Adapter) WaitForMessage(ctx context.Context, mcid cid.Cid, cb func(exitcode.ExitCode, []byte, cid.Cid, error) error) error {
	receipt, err := a.fAPI.StateWaitMsg(ctx, mcid, uint64(5))
	if err != nil {
		return cb(0, nil, cid.Undef, err)
	}
	return cb(receipt.Receipt.ExitCode, receipt.Receipt.Return, receipt.Message, nil)
}

// SignBytes signs the given data with the given address's private key
func (a *Adapter) SignBytes(ctx context.Context, signer address.Address, b []byte) (*crypto.Signature, error) {
	signer, err := a.fAPI.StateAccountKey(ctx, signer, fil.EmptyTSK)
	if err != nil {
		return nil, err
	}

	sig, err := a.wallet.Sign(ctx, signer, b)
	if err != nil {
		return nil, err
	}
	return sig, nil
}

const clientOverestimation = 2

// DealProviderCollateralBounds returns the min and max collateral a storage provider can issue.
func (a *Adapter) DealProviderCollateralBounds(ctx context.Context, size abi.PaddedPieceSize, isVerified bool) (abi.TokenAmount, abi.TokenAmount, error) {
	bounds, err := a.fAPI.StateDealProviderCollateralBounds(ctx, size, isVerified, fil.EmptyTSK)
	if err != nil {
		return big.Zero(), big.Zero(), err
	}

	return big.Mul(bounds.Min, big.NewInt(clientOverestimation)), bounds.Max, nil
}

// OnDealSectorPreCommitted waits for a deal's sector to be pre-committed
// TODO
func (a *Adapter) OnDealSectorPreCommitted(ctx context.Context, provider address.Address, dealID abi.DealID, proposal market3.DealProposal, publishCid *cid.Cid, cb sm.DealSectorPreCommittedCallback) error {
	return nil
}

// OnDealSectorCommitted waits for a deal's sector to be sealed and proved, indicating the deal is active
// TODO
func (a *Adapter) OnDealSectorCommitted(ctx context.Context, provider address.Address, dealID abi.DealID, sectorNumber abi.SectorNumber, proposal market3.DealProposal, publishCid *cid.Cid, cb sm.DealSectorCommittedCallback) error {
	return nil
}

// OnDealExpiredOrSlashed registers callbacks to be called when the deal expires or is slashed
// TODO
func (a *Adapter) OnDealExpiredOrSlashed(ctx context.Context, dealID abi.DealID, onDealExpired sm.DealExpiredCallback, onDealSlashed sm.DealSlashedCallback) error {
	return nil
}

// ListStorageProviders returns information about known miners
func (a *Adapter) ListStorageProviders(ctx context.Context, tok shared.TipSetToken) ([]*sm.StorageProviderInfo, error) {
	tsk, err := fil.TipSetKeyFromBytes(tok)
	if err != nil {
		return nil, err
	}

	addresses, err := a.fAPI.StateListMiners(ctx, tsk)
	if err != nil {
		return nil, err
	}

	var out []*sm.StorageProviderInfo

	for _, addr := range addresses {
		mi, err := a.GetMinerInfo(ctx, addr, tok)
		if err != nil {
			return nil, err
		}

		out = append(out, mi)
	}

	return out, nil
}

// ValidatePublishedDeal verifies a deal is published on chain and returns the dealID
func (a *Adapter) ValidatePublishedDeal(ctx context.Context, deal sm.ClientDeal) (abi.DealID, error) {
	pubmsg, err := a.fAPI.ChainGetMessage(ctx, *deal.PublishMessage)
	if err != nil {
		return 0, fmt.Errorf("getting deal publish message: %w", err)
	}

	mi, err := a.fAPI.StateMinerInfo(ctx, deal.Proposal.Provider, fil.EmptyTSK)
	if err != nil {
		return 0, fmt.Errorf("getting miner worker failed: %w", err)
	}

	fromid, err := a.fAPI.StateLookupID(ctx, pubmsg.From, fil.EmptyTSK)
	if err != nil {
		return 0, fmt.Errorf("failed to resolve from msg ID addr: %w", err)
	}

	if fromid != mi.Worker {
		return 0, fmt.Errorf("deal wasn't published by storage provider: from=%s, provider=%s", pubmsg.From, deal.Proposal.Provider)
	}

	if pubmsg.To != miner3.StorageMarketActorAddr {
		return 0, fmt.Errorf("deal publish message wasn't set to StorageMarket actor (to=%s)", pubmsg.To)
	}

	if pubmsg.Method != miner3.MethodsMarket.PublishStorageDeals {
		return 0, fmt.Errorf("deal publish message called incorrect method (method=%s)", pubmsg.Method)
	}

	var params market3.PublishStorageDealsParams
	if err := params.UnmarshalCBOR(bytes.NewReader(pubmsg.Params)); err != nil {
		return 0, err
	}

	dealIdx := -1
	for i, storageDeal := range params.Deals {
		// TODO: make it less hacky
		sd := storageDeal
		eq, err := cborutil.Equals(&deal.ClientDealProposal, &sd)
		if err != nil {
			return 0, err
		}
		if eq {
			dealIdx = i
			break
		}
	}

	if dealIdx == -1 {
		return 0, fmt.Errorf("deal publish didn't contain our deal (message cid: %s)", deal.PublishMessage)
	}

	// TODO: timeout
	ret, err := a.fAPI.StateWaitMsg(ctx, *deal.PublishMessage, uint64(5))
	if err != nil {
		return 0, fmt.Errorf("waiting for deal publish message: %w", err)
	}
	if ret.Receipt.ExitCode != 0 {
		return 0, fmt.Errorf("deal publish failed: exit=%d", ret.Receipt.ExitCode)
	}

	var res market3.PublishStorageDealsReturn
	if err := res.UnmarshalCBOR(bytes.NewReader(ret.Receipt.Return)); err != nil {
		return 0, err
	}

	return res.IDs[dealIdx], nil
}

// SignProposal signs a DealProposal
func (a *Adapter) SignProposal(ctx context.Context, signer address.Address, proposal market3.DealProposal) (*market3.ClientDealProposal, error) {
	// TODO: output spec signed proposal
	buf, err := cborutil.Dump(&proposal)
	if err != nil {
		return nil, err
	}

	signer, err = a.fAPI.StateAccountKey(ctx, signer, fil.EmptyTSK)
	if err != nil {
		return nil, err
	}

	sig, err := a.wallet.Sign(ctx, signer, buf)
	if err != nil {
		return nil, err
	}

	return &market3.ClientDealProposal{
		Proposal:        proposal,
		ClientSignature: *sig,
	}, nil
}

// GetDefaultWalletAddress returns the address for this client
func (a *Adapter) GetDefaultWalletAddress(ctx context.Context) (address.Address, error) {
	return a.wallet.DefaultAddress(), nil
}

// GetMinerInfo returns info for a single miner with the given address
func (a *Adapter) GetMinerInfo(ctx context.Context, maddr address.Address, tok shared.TipSetToken) (*sm.StorageProviderInfo, error) {
	tsk, err := fil.TipSetKeyFromBytes(tok)
	if err != nil {
		return nil, err
	}
	mi, err := a.fAPI.StateMinerInfo(ctx, maddr, tsk)
	if err != nil {
		return nil, err
	}
	out := NewStorageProviderInfo(maddr, mi.Worker, mi.SectorSize, *mi.PeerId, mi.Multiaddrs)
	return &out, nil
}

// NewStorageProviderInfo transforms our MinerInfo struct into a StorageProviderInfo from storagemarket
func NewStorageProviderInfo(address address.Address, miner address.Address, sectorSize abi.SectorSize, peer peer.ID, addrs []abi.Multiaddrs) sm.StorageProviderInfo {
	multiaddrs := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, a := range addrs {
		maddr, err := multiaddr.NewMultiaddrBytes(a)
		if err != nil {
			return sm.StorageProviderInfo{}
		}
		multiaddrs = append(multiaddrs, maddr)
	}

	return sm.StorageProviderInfo{
		Address:    address,
		Worker:     miner,
		SectorSize: uint64(sectorSize),
		PeerID:     peer,
		Addrs:      multiaddrs,
	}
}
