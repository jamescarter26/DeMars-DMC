package mempool

import (
	"fmt"
	"reflect"
	"time"

	amino "github.com/tendermint/go-amino"
	abci "github.com/Demars-DMC/Demars-DMC/abci/types"
	"github.com/Demars-DMC/Demars-DMC/libs/clist"
	"github.com/Demars-DMC/Demars-DMC/libs/log"

	cfg "github.com/Demars-DMC/Demars-DMC/config"
	"github.com/Demars-DMC/Demars-DMC/p2p"
	"github.com/Demars-DMC/Demars-DMC/types"
)

const (
	MempoolChannel = byte(0x30)

	maxMsgSize                 = 1048576 // 1MB TODO make it configurable
	peerCatchupSleepIntervalMS = 100     // If peer is behind, sleep this amount
)

// MempoolReactor handles mempool tx broadcasting amongst peers.
type MempoolReactor struct {
	p2p.BaseReactor
	config  *cfg.MempoolConfig
	Mempool *Mempool
	UnprocessedTxs []types.Tx
}

// NewMempoolReactor returns a new MempoolReactor with the given config and mempool.
func NewMempoolReactor(config *cfg.MempoolConfig, mempool *Mempool) *MempoolReactor {
	memR := &MempoolReactor{
		config:  config,
		Mempool: mempool,
		UnprocessedTxs: make([]types.Tx, 100),
	}
	memR.BaseReactor = *p2p.NewBaseReactor("MempoolReactor", memR)
	return memR
}

// SetLogger sets the Logger on the reactor and the underlying Mempool.
func (memR *MempoolReactor) SetLogger(l log.Logger) {
	memR.Logger = l
	memR.Mempool.SetLogger(l)
}

// OnStart implements p2p.BaseReactor.
func (memR *MempoolReactor) OnStart() error {
	if !memR.config.Broadcast {
		memR.Logger.Info("Tx broadcasting is disabled")
	}

	go memR.poolRoutine()

	return nil
}

func (memR *MempoolReactor) poolRoutine() {
	checkUnprocessedTxs := time.NewTicker(50 * time.Millisecond)
	for {
		select {
		case <-checkUnprocessedTxs.C:
			for i := 0; i < 100; i++ {
				if memR.UnprocessedTxs[i] != nil {
					err, bucketID := memR.Mempool.CheckTx(memR.UnprocessedTxs[i], nil)
					if err != nil {
						memR.Logger.Info("Could not check tx", "tx", TxID(memR.UnprocessedTxs[i]), "err", err)
					}
					if bucketID == "" {
						// Transaction has been checked
						memR.UnprocessedTxs[i] = nil
					}
				}
			}
		}
	}
}

// GetChannels implements Reactor.
// It returns the list of channels for this reactor.
func (memR *MempoolReactor) GetChannels() []*p2p.ChannelDescriptor {
	return []*p2p.ChannelDescriptor{
		{
			ID:       MempoolChannel,
			Priority: 5,
		},
	}
}

// AddPeer implements Reactor.
// It starts a broadcast routine ensuring all txs are forwarded to the given peer.
func (memR *MempoolReactor) AddPeer(peer p2p.Peer) {
	go memR.broadcastTxRoutine(peer)
}

// RemovePeer implements Reactor.
func (memR *MempoolReactor) RemovePeer(peer p2p.Peer, reason interface{}) {
	// broadcast routine checks if peer is gone and returns
}

// Receive implements Reactor.
// It adds any received transactions to the mempool.
func (memR *MempoolReactor) Receive(chID byte, src p2p.Peer, msgBytes []byte) {
	msg, err := DecodeMessage(msgBytes)
	if err != nil {
		memR.Logger.Error("Error decoding message", "src", src, "chId", chID, "msg", msg, "err", err, "bytes", msgBytes)
		memR.Switch.StopPeerForError(src, err)
		return
	}
	memR.Logger.Debug("Receive", "src", src, "chId", chID, "msg", msg)

	switch msg := msg.(type) {
	case *TxMessage:
		err, bucketID := memR.Mempool.CheckTx(msg.Tx, nil)
		if err != nil {
			memR.Logger.Info("Could not check tx", "tx", TxID(msg.Tx), "err", err)
		}
		if bucketID != "" {
			// We need to wait for this bucket chain to be retrieved from peers
			for i := 0; i < 100; i++ {
				if memR.UnprocessedTxs[i] == nil {
					memR.UnprocessedTxs[i] = msg.Tx
				}
			}
		}
		// broadcasting happens from go routines per peer
	default:
		memR.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
	}
}

// BroadcastTx is an alias for Mempool.CheckTx. Broadcasting itself happens in peer routines.
func (memR *MempoolReactor) BroadcastTx(tx types.Tx, cb func(*abci.Response)) (error, string) {
	return memR.Mempool.CheckTx(tx, cb)
}

// PeerState describes the state of a peer.
type PeerState interface {
	GetHeight() int64
}

// Send new mempool txs to peer.
func (memR *MempoolReactor) broadcastTxRoutine(peer p2p.Peer) {
	if !memR.config.Broadcast {
		return
	}

	var next *clist.CElement
	for {
		// This happens because the CElement we were looking at got garbage
		// collected (removed). That is, .NextWait() returned nil. Go ahead and
		// start from the beginning.
		if next == nil {
			select {
			case <-memR.Mempool.TxsWaitChan(): // Wait until a tx is available
				if next = memR.Mempool.TxsFront(); next == nil {
					continue
				}
			case <-peer.Quit():
				return
			case <-memR.Quit():
				return
			}
		}

		memTx := next.Value.(*mempoolTx)
		// make sure the peer is up to date
		height := memTx.Height()
		if peerState_i := peer.Get(types.PeerStateKey); peerState_i != nil {
			peerState := peerState_i.(PeerState)
			peerHeight := peerState.GetHeight()
			if peerHeight < height-1 { // Allow for a lag of 1 block
				time.Sleep(peerCatchupSleepIntervalMS * time.Millisecond)
				continue
			}
		}
		// send memTx
		msg := &TxMessage{Tx: memTx.tx}
		success := peer.Send(MempoolChannel, cdc.MustMarshalBinaryBare(msg))
		if !success {
			time.Sleep(peerCatchupSleepIntervalMS * time.Millisecond)
			continue
		}

		select {
		case <-next.NextWaitChan():
			// see the start of the for loop for nil check
			next = next.Next()
		case <-peer.Quit():
			return
		case <-memR.Quit():
			return
		}
	}
}

//-----------------------------------------------------------------------------
// Messages

// MempoolMessage is a message sent or received by the MempoolReactor.
type MempoolMessage interface{}

func RegisterMempoolMessages(cdc *amino.Codec) {
	cdc.RegisterInterface((*MempoolMessage)(nil), nil)
	cdc.RegisterConcrete(&TxMessage{}, "Demars-DMC/mempool/TxMessage", nil)
}

// DecodeMessage decodes a byte-array into a MempoolMessage.
func DecodeMessage(bz []byte) (msg MempoolMessage, err error) {
	if len(bz) > maxMsgSize {
		return msg, fmt.Errorf("Msg exceeds max size (%d > %d)",
			len(bz), maxMsgSize)
	}
	err = cdc.UnmarshalBinaryBare(bz, &msg)
	return
}

//-------------------------------------

// TxMessage is a MempoolMessage containing a transaction.
type TxMessage struct {
	Tx types.Tx
}

// String returns a string representation of the TxMessage.
func (m *TxMessage) String() string {
	return fmt.Sprintf("[TxMessage %v]", m.Tx)
}
