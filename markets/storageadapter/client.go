package storageadapter

// this file implements storagemarket.StorageClientNode

import (
	"bytes"
	"context"

	"golang.org/x/xerrors"

	"github.com/filecoin-project/go-address"
	cborutil "github.com/filecoin-project/go-cbor-util"
	"github.com/filecoin-project/go-fil-markets/shared"
	"github.com/filecoin-project/go-fil-markets/storagemarket"
	"github.com/filecoin-project/specs-actors/actors/abi"
	"github.com/filecoin-project/specs-actors/actors/builtin"
	samarket "github.com/filecoin-project/specs-actors/actors/builtin/market"
	"github.com/filecoin-project/specs-actors/actors/builtin/miner"
	"github.com/filecoin-project/specs-actors/actors/crypto"

	"github.com/filecoin-project/lotus/build"
	"github.com/filecoin-project/lotus/chain/events"
	"github.com/filecoin-project/lotus/chain/market"
	"github.com/filecoin-project/lotus/chain/stmgr"
	"github.com/filecoin-project/lotus/chain/store"
	"github.com/filecoin-project/lotus/chain/types"
	"github.com/filecoin-project/lotus/lib/sigs"
	"github.com/filecoin-project/lotus/markets/utils"
	"github.com/filecoin-project/lotus/node/impl/full"
)

type ClientNodeAdapter struct {
	full.StateAPI
	full.ChainAPI
	full.MpoolAPI

	sm *stmgr.StateManager
	cs *store.ChainStore
	fm *market.FundMgr
	ev *events.Events
}

type clientApi struct {
	full.ChainAPI
	full.StateAPI
}

func NewClientNodeAdapter(state full.StateAPI, chain full.ChainAPI, mpool full.MpoolAPI, sm *stmgr.StateManager, cs *store.ChainStore, fm *market.FundMgr) storagemarket.StorageClientNode {
	return &ClientNodeAdapter{
		StateAPI: state,
		ChainAPI: chain,
		MpoolAPI: mpool,

		sm: sm,
		cs: cs,
		fm: fm,
		ev: events.NewEvents(context.TODO(), &clientApi{chain, state}),
	}
}

func (n *ClientNodeAdapter) ListStorageProviders(ctx context.Context, encodedTs shared.TipSetToken) ([]*storagemarket.StorageProviderInfo, error) {
	tsk, err := types.TipSetKeyFromBytes(encodedTs)
	if err != nil {
		return nil, err
	}

	addresses, err := n.StateListMiners(ctx, tsk)
	if err != nil {
		return nil, err
	}

	var out []*storagemarket.StorageProviderInfo

	for _, addr := range addresses {
		workerAddr, err := n.StateMinerWorker(ctx, addr, tsk)
		if err != nil {
			return nil, err
		}

		sectorSize, err := n.StateMinerSectorSize(ctx, addr, tsk)
		if err != nil {
			return nil, err
		}

		peerID, err := n.StateMinerPeerID(ctx, addr, tsk)
		if err != nil {
			return nil, err
		}
		storageProviderInfo := utils.NewStorageProviderInfo(addr, workerAddr, sectorSize, peerID)
		out = append(out, &storageProviderInfo)
	}

	return out, nil
}

func (n *ClientNodeAdapter) VerifySignature(ctx context.Context, sig crypto.Signature, addr address.Address, input []byte, encodedTs shared.TipSetToken) (bool, error) {
	err := sigs.Verify(&sig, addr, input)
	return err == nil, err
}

func (n *ClientNodeAdapter) ListClientDeals(ctx context.Context, addr address.Address, encodedTs shared.TipSetToken) ([]storagemarket.StorageDeal, error) {
	tsk, err := types.TipSetKeyFromBytes(encodedTs)
	if err != nil {
		return nil, err
	}

	allDeals, err := n.StateMarketDeals(ctx, tsk)
	if err != nil {
		return nil, err
	}

	var out []storagemarket.StorageDeal

	for _, deal := range allDeals {
		storageDeal := utils.FromOnChainDeal(deal.Proposal, deal.State)
		if storageDeal.Client == addr {
			out = append(out, storageDeal)
		}
	}

	return out, nil
}

// Adds funds with the StorageMinerActor for a storage participant.  Used by both providers and clients.
func (n *ClientNodeAdapter) AddFunds(ctx context.Context, addr address.Address, amount abi.TokenAmount) error {
	// (Provider Node API)
	smsg, err := n.MpoolPushMessage(ctx, &types.Message{
		To:       builtin.StorageMarketActorAddr,
		From:     addr,
		Value:    amount,
		GasPrice: types.NewInt(0),
		GasLimit: 1000000,
		Method:   builtin.MethodsMarket.AddBalance,
	})
	if err != nil {
		return err
	}

	r, err := n.StateWaitMsg(ctx, smsg.Cid())
	if err != nil {
		return err
	}

	if r.Receipt.ExitCode != 0 {
		return xerrors.Errorf("adding funds to storage miner market actor failed: exit %d", r.Receipt.ExitCode)
	}

	return nil
}

func (n *ClientNodeAdapter) EnsureFunds(ctx context.Context, addr, wallet address.Address, amount abi.TokenAmount, ts shared.TipSetToken) error {
	return n.fm.EnsureAvailable(ctx, addr, wallet, amount)
}

func (n *ClientNodeAdapter) GetBalance(ctx context.Context, addr address.Address, encodedTs shared.TipSetToken) (storagemarket.Balance, error) {
	tsk, err := types.TipSetKeyFromBytes(encodedTs)
	if err != nil {
		return storagemarket.Balance{}, err
	}

	bal, err := n.StateMarketBalance(ctx, addr, tsk)
	if err != nil {
		return storagemarket.Balance{}, err
	}

	return utils.ToSharedBalance(bal), nil
}

// ValidatePublishedDeal validates that the provided deal has appeared on chain and references the same ClientDeal
// returns the Deal id if there is no error
func (c *ClientNodeAdapter) ValidatePublishedDeal(ctx context.Context, deal storagemarket.ClientDeal) (abi.DealID, error) {
	log.Infow("DEAL ACCEPTED!")

	pubmsg, err := c.cs.GetMessage(*deal.PublishMessage)
	if err != nil {
		return 0, xerrors.Errorf("getting deal pubsish message: %w", err)
	}

	pw, err := stmgr.GetMinerWorker(ctx, c.sm, nil, deal.Proposal.Provider)
	if err != nil {
		return 0, xerrors.Errorf("getting miner worker failed: %w", err)
	}

	if pubmsg.From != pw {
		return 0, xerrors.Errorf("deal wasn't published by storage provider: from=%s, provider=%s", pubmsg.From, deal.Proposal.Provider)
	}

	if pubmsg.To != builtin.StorageMarketActorAddr {
		return 0, xerrors.Errorf("deal publish message wasn't set to StorageMarket actor (to=%s)", pubmsg.To)
	}

	if pubmsg.Method != builtin.MethodsMarket.PublishStorageDeals {
		return 0, xerrors.Errorf("deal publish message called incorrect method (method=%s)", pubmsg.Method)
	}

	var params samarket.PublishStorageDealsParams
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
		return 0, xerrors.Errorf("deal publish didn't contain our deal (message cid: %s)", deal.PublishMessage)
	}

	// TODO: timeout
	_, ret, err := c.sm.WaitForMessage(ctx, *deal.PublishMessage)
	if err != nil {
		return 0, xerrors.Errorf("waiting for deal publish message: %w", err)
	}
	if ret.ExitCode != 0 {
		return 0, xerrors.Errorf("deal publish failed: exit=%d", ret.ExitCode)
	}

	var res samarket.PublishStorageDealsReturn
	if err := res.UnmarshalCBOR(bytes.NewReader(ret.Return)); err != nil {
		return 0, err
	}

	return res.IDs[dealIdx], nil
}

func (c *ClientNodeAdapter) OnDealSectorCommitted(ctx context.Context, provider address.Address, dealId abi.DealID, cb storagemarket.DealSectorCommittedCallback) error {
	checkFunc := func(ts *types.TipSet) (done bool, more bool, err error) {
		sd, err := stmgr.GetStorageDeal(ctx, c.StateManager, dealId, ts)

		if err != nil {
			// TODO: This may be fine for some errors
			return false, false, xerrors.Errorf("failed to look up deal on chain: %w", err)
		}

		if sd.State.SectorStartEpoch > 0 {
			cb(nil)
			return true, false, nil
		}

		return false, true, nil
	}

	called := func(msg *types.Message, rec *types.MessageReceipt, ts *types.TipSet, curH abi.ChainEpoch) (more bool, err error) {
		defer func() {
			if err != nil {
				cb(xerrors.Errorf("handling applied event: %w", err))
			}
		}()

		if msg == nil {
			log.Error("timed out waiting for deal activation... what now?")
			return false, nil
		}

		sd, err := stmgr.GetStorageDeal(ctx, c.StateManager, abi.DealID(dealId), ts)
		if err != nil {
			return false, xerrors.Errorf("failed to look up deal on chain: %w", err)
		}

		if sd.State.SectorStartEpoch < 1 {
			return false, xerrors.Errorf("deal wasn't active: deal=%d, parentState=%s, h=%d", dealId, ts.ParentState(), ts.Height())
		}

		log.Infof("Storage deal %d activated at epoch %d", dealId, sd.State.SectorStartEpoch)

		cb(nil)

		return false, nil
	}

	revert := func(ctx context.Context, ts *types.TipSet) error {
		log.Warn("deal activation reverted; TODO: actually handle this!")
		// TODO: Just go back to DealSealing?
		return nil
	}

	var sectorNumber abi.SectorNumber
	var sectorFound bool
	matchEvent := func(msg *types.Message) (bool, error) {
		if msg.To != provider {
			return false, nil
		}

		switch msg.Method {
		case builtin.MethodsMiner.PreCommitSector:
			var params miner.SectorPreCommitInfo
			if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
				return false, xerrors.Errorf("unmarshal pre commit: %w", err)
			}

			for _, did := range params.DealIDs {
				if did == abi.DealID(dealId) {
					sectorNumber = params.SectorNumber
					sectorFound = true
					return false, nil
				}
			}

			return false, nil
		case builtin.MethodsMiner.ProveCommitSector:
			var params miner.ProveCommitSectorParams
			if err := params.UnmarshalCBOR(bytes.NewReader(msg.Params)); err != nil {
				return false, xerrors.Errorf("failed to unmarshal prove commit sector params: %w", err)
			}

			if !sectorFound {
				return false, nil
			}

			if params.SectorNumber != sectorNumber {
				return false, nil
			}

			return true, nil
		default:
			return false, nil
		}
	}

	if err := c.ev.Called(checkFunc, called, revert, 3, build.SealRandomnessLookbackLimit, matchEvent); err != nil {
		return xerrors.Errorf("failed to set up called handler: %w", err)
	}

	return nil
}

func (n *ClientNodeAdapter) SignProposal(ctx context.Context, signer address.Address, proposal samarket.DealProposal) (*samarket.ClientDealProposal, error) {
	// TODO: output spec signed proposal
	buf, err := cborutil.Dump(&proposal)
	if err != nil {
		return nil, err
	}

	sig, err := n.Wallet.Sign(ctx, signer, buf)
	if err != nil {
		return nil, err
	}

	return &samarket.ClientDealProposal{
		Proposal:        proposal,
		ClientSignature: *sig,
	}, nil
}

func (n *ClientNodeAdapter) GetDefaultWalletAddress(ctx context.Context) (address.Address, error) {
	addr, err := n.Wallet.GetDefault()
	return addr, err
}

func (n *ClientNodeAdapter) ValidateAskSignature(ctx context.Context, ask *storagemarket.SignedStorageAsk, encodedTs shared.TipSetToken) (bool, error) {
	tsk, err := types.TipSetKeyFromBytes(encodedTs)
	if err != nil {
		return false, err
	}

	w, err := n.StateMinerWorker(ctx, ask.Ask.Miner, tsk)
	if err != nil {
		return false, xerrors.Errorf("failed to get worker for miner in ask", err)
	}

	sigb, err := cborutil.Dump(ask.Ask)
	if err != nil {
		return false, xerrors.Errorf("failed to re-serialize ask")
	}

	err = sigs.Verify(ask.Signature, w, sigb)
	return err == nil, err
}

func (n *ClientNodeAdapter) GetChainHead(ctx context.Context) (shared.TipSetToken, abi.ChainEpoch, error) {
	head, err := n.ChainHead(ctx)
	if err != nil {
		return nil, 0, err
	}

	return head.Key().Bytes(), head.Height(), nil
}

var _ storagemarket.StorageClientNode = &ClientNodeAdapter{}
