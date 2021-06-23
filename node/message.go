package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/rs/zerolog/log"
)

var jsonEscapedZero = []byte(`\u0000`)

// PingArgs get passed to the Ping command
type PingArgs struct {
	Addr string
}

// PutArgs get passed to the Put command
type PutArgs struct {
	Path      string
	ChunkSize int
}

// StatusArgs get passed to the Status command
type StatusArgs struct {
	Verbose bool
}

// WalletListArgs get passed to the WalletList command
type WalletListArgs struct{}

// WalletExportArgs get passed to the WalletExport command
type WalletExportArgs struct {
	Address    string
	OutputPath string
}

// WalletPayArgs get passed to the WalletPay command
type WalletPayArgs struct {
	From   string
	To     string
	Amount string
}

// QuoteArgs are passed to the quote command
type QuoteArgs struct {
	Refs      []string
	StorageRF int // StorageRF is the replication factor or number of miners we will try to store with
	Duration  time.Duration
	MaxPrice  uint64
}

// CommArgs are passed to the Commit command
type CommArgs struct {
	CacheRF int // CacheRF is the cache replication factor or number of cache provider will request
}

// StoreArgs are arguments for the Store command
type StoreArgs struct {
	Refs      []string // Ref is the root CID of the archive to push to remote storage
	StorageRF int      // StorageRF if the replication factor for storage
	Duration  time.Duration
	Miners    map[string]bool
	MaxPrice  uint64
	Verified  bool
}

// GetArgs get passed to the Get command
type GetArgs struct {
	Cid      string
	Key      string
	Sel      string
	Out      string
	Timeout  int
	Verbose  bool
	Miner    string
	Strategy string
}

// ListArgs provides params for the List command
type ListArgs struct {
	Page int // potential pagination as the amount may be very large
}

// Command is a message sent from a client to the daemon
type Command struct {
	Ping             *PingArgs
	Put              *PutArgs
	Status           *StatusArgs
	WalletListArgs   *WalletListArgs
	WalletExportArgs *WalletExportArgs
	WalletPayArgs    *WalletPayArgs
	Quote            *QuoteArgs
	Commit           *CommArgs
	Store            *StoreArgs
	Get              *GetArgs
	List             *ListArgs
}

// PingResult is sent in the notify message to give us the info we requested
type PingResult struct {
	ID             string   // Host's peer ID
	Addrs          []string // Addresses the host is listening on
	Peers          []string // Peers currently connected to the node (local daemon only)
	LatencySeconds float64
	Version        string // The Version the node is running
	Err            string
}

// PutResult gives us feedback on the result of the Put request
type PutResult struct {
	RootCid   string
	Key       string
	Cid       string
	Size      string
	TotalSize string
	Len       int
	Err       string
}

// StatusResult gives us the result of status request to ping
type StatusResult struct {
	RootCid string
	Entries string
	Err     string
}

// WalletResult returns the output of every WalletList/WalletExport/WalletPay requests
type WalletResult struct {
	Err       string
	Addresses []string
}

// QuoteResult returns the output of the Quote request
type QuoteResult struct {
	Ref         string
	Quotes      map[string]string
	PayloadSize uint64
	PieceSize   uint64
	Err         string
}

// CommResult is feedback on the push operation
type CommResult struct {
	Ref    string
	Caches []string
	Size   string
	Err    string
}

// StoreResult returns the result of the storage operation
type StoreResult struct {
	Miners   []string
	Deals    []string
	Capacity uint64 // Capacity is the space left before it is possible to store on Filecoin
	Err      string
}

// GetResult gives us feedback on the result of the Get request
type GetResult struct {
	DealID          string
	TotalSpent      string
	TotalPrice      string
	PieceSize       string
	PricePerByte    string
	UnsealPrice     string
	DiscLatSeconds  float64
	TransLatSeconds float64
	Local           bool
	Err             string
}

// ListResult contains the result for a single item of the list
type ListResult struct {
	Root string
	Freq int64
	Size int64
	Last bool
	Err  string
}

// Notify is a message sent from the daemon to the client
type Notify struct {
	PingResult   *PingResult
	PutResult    *PutResult
	StatusResult *StatusResult
	WalletResult *WalletResult
	QuoteResult  *QuoteResult
	CommResult   *CommResult
	StoreResult  *StoreResult
	GetResult    *GetResult
	ListResult   *ListResult
}

// CommandServer receives commands on the daemon side and executes them
type CommandServer struct {
	n             *node                // the ipfs node we are controlling
	sendNotifyMsg func(jsonMsg []byte) // send a notification message
}

func NewCommandServer(ipfs *node, sendNotifyMsg func(b []byte)) *CommandServer {
	return &CommandServer{
		n:             ipfs,
		sendNotifyMsg: sendNotifyMsg,
	}
}

func (cs *CommandServer) GotMsgBytes(ctx context.Context, b []byte) error {
	cmd := &Command{}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, cmd); err != nil {
		return err
	}
	return cs.GotMsg(ctx, cmd)
}

func (cs *CommandServer) GotMsg(ctx context.Context, cmd *Command) error {
	if c := cmd.Ping; c != nil {
		cs.n.Ping(ctx, c.Addr)
		return nil
	}
	if c := cmd.Put; c != nil {
		cs.n.Put(ctx, c)
		return nil
	}
	if c := cmd.Status; c != nil {
		cs.n.Status(ctx, c)
		return nil
	}
	if c := cmd.WalletListArgs; c != nil {
		cs.n.WalletList(ctx, c)
		return nil
	}
	if c := cmd.WalletExportArgs; c != nil {
		cs.n.WalletExport(ctx, c)
		return nil
	}
	if c := cmd.WalletPayArgs; c != nil {
		cs.n.WalletPay(ctx, c)
		return nil
	}
	if c := cmd.Quote; c != nil {
		cs.n.Quote(ctx, c)
		return nil
	}
	if c := cmd.Commit; c != nil {
		// push requests are usually quite long so we don't block the thread so users
		// can start a new transaction while their previous commit is uploading for example
		go cs.n.Commit(ctx, c)
		return nil
	}
	if c := cmd.Store; c != nil {
		cs.n.Store(ctx, c)
		return nil
	}
	if c := cmd.Get; c != nil {
		// Get requests can be quite long and we don't want to block other commands
		go cs.n.Get(ctx, c)
		return nil
	}
	if c := cmd.List; c != nil {
		go cs.n.List(ctx, c)
		return nil
	}
	return fmt.Errorf("CommandServer: no command specified")
}

func (cs *CommandServer) send(n Notify) {
	b, err := json.Marshal(n)
	if err != nil {
		log.Fatal().Err(err).Interface("n", n).Msg("Failed json.Marshal(notify)")
	}
	if bytes.Contains(b, jsonEscapedZero) {
		log.Error().Msg("[unexpected] zero byte in BackendServer.send notify message")
	}
	cs.sendNotifyMsg(b)
}

// CommandClient sends commands to a daemon process
type CommandClient struct {
	sendCommandMsg func(jsonb []byte)
	notify         func(Notify)
}

func NewCommandClient(sendCommandMsg func(jsonb []byte)) *CommandClient {
	return &CommandClient{
		sendCommandMsg: sendCommandMsg,
	}
}

func (cc *CommandClient) GotNotifyMsg(b []byte) {
	if len(b) == 0 {
		// not interesting
		return
	}
	if bytes.Contains(b, jsonEscapedZero) {
		log.Error().Msg("[unexpected] zero byte in BackendClient.GotNotifyMsg message")
	}
	n := Notify{}
	if err := json.Unmarshal(b, &n); err != nil {
		log.Fatal().Err(err).Int("len", len(b)).Msg("BackendClient.Notify: cannot decode message")
	}
	if cc.notify != nil {
		cc.notify(n)
	}
}

func (cc *CommandClient) send(cmd Command) {
	b, err := json.Marshal(cmd)
	if err != nil {
		log.Error().Err(err).Msg("Failed json.Marshal(cmd)")
	}
	if bytes.Contains(b, jsonEscapedZero) {
		log.Error().Err(err).Msg("[unexpected] zero byte in CommandClient.send")
	}
	cc.sendCommandMsg(b)
}

func (cc *CommandClient) Ping(addr string) {
	cc.send(Command{Ping: &PingArgs{Addr: addr}})
}

func (cc *CommandClient) Put(args *PutArgs) {
	cc.send(Command{Put: args})
}

func (cc *CommandClient) Status(args *StatusArgs) {
	cc.send(Command{Status: args})
}

func (cc *CommandClient) WalletListKeys(args *WalletListArgs) {
	cc.send(Command{WalletListArgs: args})
}

func (cc *CommandClient) WalletExport(args *WalletExportArgs) {
	cc.send(Command{WalletExportArgs: args})
}

func (cc *CommandClient) WalletPay(args *WalletPayArgs) {
	cc.send(Command{WalletPayArgs: args})
}

func (cc *CommandClient) Quote(args *QuoteArgs) {
	cc.send(Command{Quote: args})
}

func (cc *CommandClient) Commit(args *CommArgs) {
	cc.send(Command{Commit: args})
}

func (cc *CommandClient) Store(args *StoreArgs) {
	cc.send(Command{Store: args})
}

func (cc *CommandClient) Get(args *GetArgs) {
	cc.send(Command{Get: args})
}

func (cc *CommandClient) List(args *ListArgs) {
	cc.send(Command{List: args})
}

func (cc *CommandClient) SetNotifyCallback(fn func(Notify)) {
	cc.notify = fn
}

// MaxMessageSize is the maximum message size, in bytes.
const MaxMessageSize = 10 << 20

func ReadMsg(r io.Reader) ([]byte, error) {
	cb := make([]byte, 4)
	_, err := io.ReadFull(r, cb)
	if err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(cb)
	if n > MaxMessageSize {
		return nil, fmt.Errorf("ReadMsg: message too large: %d bytes", n)
	}
	b := make([]byte, n)
	nn, err := io.ReadFull(r, b)
	if err != nil {
		return nil, err
	}
	if nn != int(n) {
		return nil, fmt.Errorf("ReadMsg: expected %d bytes, got %d", n, nn)
	}
	return b, nil
}

func WriteMsg(w io.Writer, b []byte) error {
	cb := make([]byte, 4)
	if len(b) > MaxMessageSize {
		return fmt.Errorf("WriteMsg: message too large: %d bytes", len(b))
	}
	binary.LittleEndian.PutUint32(cb, uint32(len(b)))
	n, err := w.Write(cb)
	if err != nil {
		return err
	}
	if n != 4 {
		return fmt.Errorf("WriteMsg: short write: %d bytes (wanted 4)", n)
	}
	n, err = w.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return fmt.Errorf("WriteMsg: short write: %d bytes (wanted %d)", n, len(b))
	}
	return nil
}
