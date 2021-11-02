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

// OffArgs get passed to the Off command
type OffArgs struct{}

// PingArgs get passed to the Ping command
type PingArgs struct {
	Addr string
}

// PutArgs get passed to the Put command
type PutArgs struct {
	Path      string
	ChunkSize int
	Codec     uint64
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

// CommArgs are passed to the Commit command
type CommArgs struct {
	CacheRF    int // CacheRF is the cache replication factor or number of cache provider will request
	Attempts   int
	BackoffMin time.Duration
	Peers      []string
}

// GetArgs get passed to the Get command
type GetArgs struct {
	Cid      string `json:"cid"`
	Key      string `json:"key,omitempty"`
	Sel      string `json:"sel,omitempty"`
	Out      string `json:"out,omitempty"`
	Timeout  int    `json:"timeout,omitempty"`
	Verbose  bool   `json:"verbose,omitempty"`
	Miner    string `json:"miner,omitempty"`
	Peer     string `json:"peer,omitempty"`
	Strategy string `json:"strategy,omitempty"`
	MaxPPB   int64  `json:"maxPPB,omitempty"`
}

// ListArgs provides params for the List command
type ListArgs struct {
	Page int // potential pagination as the amount may be very large
}

// ImportArgs provides the path to the car file
type ImportArgs struct {
	Path       string
	CacheRF    int
	Attempts   int
	BackoffMin time.Duration
	Peers      []string
}

// PayArgs provides params for controlling a payment channel
type PayArgs struct {
	ChAddr string
	Lane   uint64
}

// Command is a message sent from a client to the daemon
type Command struct {
	Off          *OffArgs
	Ping         *PingArgs
	Put          *PutArgs
	Status       *StatusArgs
	WalletList   *WalletListArgs
	WalletExport *WalletExportArgs
	WalletPay    *WalletPayArgs
	Commit       *CommArgs
	Get          *GetArgs
	List         *ListArgs
	Import       *ImportArgs
	PaySubmit    *PayArgs
	PayList      *PayArgs
	PayTrack     *PayArgs
	PaySettle    *PayArgs
	PayCollect   *PayArgs
}

// OffResult doesn't return any value at this time
type OffResult struct{}

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

// CommResult is feedback on the push operation
type CommResult struct {
	Ref    string
	Caches []string
	Size   string
	Err    string
}

// GetResult gives us feedback on the result of the Get request
type GetResult struct {
	Status          string  `json:"status,omitempty"`
	DealID          string  `json:"dealID,omitempty"`
	Size            int64   `json:"size,omitempty"`
	TotalSpent      string  `json:"totalSpent,omitempty"`
	TotalReceived   int64   `json:"totalReceived,omitempty"`
	BytesPaidFor    string  `json:"bytesPaidFor,omitempty"`
	TotalFunds      string  `json:"totalFunds,omitempty"`
	PricePerByte    string  `json:"pricePerByte,omitempty"`
	UnsealPrice     string  `json:"unsealPrice,omitempty"`
	DiscLatSeconds  float64 `json:"discLatSeconds,omitempty"`
	TransLatSeconds float64 `json:"tansLatSeconds,omitempty"`
	Local           bool    `json:"local,omitempty"`
	Err             string  `json:"error,omitempty"`
}

// ListResult contains the result for a single item of the list
type ListResult struct {
	Root string
	Freq int64
	Size int64
	Last bool
	Err  string
}

// ImportResult returns the resulting index entries from the CAR file
type ImportResult struct {
	Roots  []string
	Caches []string
	Err    string
}

// PayResult returns the result of submitted vouchers
type PayResult struct {
	SettlingIn  string
	ChannelList string
	Err         string
}

// Notify is a message sent from the daemon to the client
type Notify struct {
	OffResult    *OffResult
	PingResult   *PingResult
	PutResult    *PutResult
	StatusResult *StatusResult
	WalletResult *WalletResult
	CommResult   *CommResult
	GetResult    *GetResult
	ListResult   *ListResult
	ImportResult *ImportResult
	PayResult    *PayResult
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
	if c := cmd.Off; c != nil {
		cs.n.Off(ctx)
		return nil
	}
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
	if c := cmd.WalletList; c != nil {
		cs.n.WalletList(ctx, c)
		return nil
	}
	if c := cmd.WalletExport; c != nil {
		cs.n.WalletExport(ctx, c)
		return nil
	}
	if c := cmd.WalletPay; c != nil {
		cs.n.WalletPay(ctx, c)
		return nil
	}
	if c := cmd.Commit; c != nil {
		// push requests are usually quite long so we don't block the thread so users
		// can start a new transaction while their previous commit is uploading for example
		go cs.n.Commit(ctx, c)
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
	if c := cmd.Import; c != nil {
		go cs.n.Import(ctx, c)
		return nil
	}
	if c := cmd.PaySubmit; c != nil {
		go cs.n.PaySubmit(ctx, c)
		return nil
	}
	if c := cmd.PayList; c != nil {
		go cs.n.PayList(ctx, c)
		return nil
	}
	if c := cmd.PaySettle; c != nil {
		go cs.n.PaySettle(ctx, c)
		return nil
	}
	if c := cmd.PayCollect; c != nil {
		go cs.n.PayCollect(ctx, c)
		return nil
	}
	if c := cmd.PayTrack; c != nil {
		cs.n.PayTrack(ctx, c)
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

func (cc *CommandClient) Off() {
	cc.send(Command{Off: &OffArgs{}})
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
	cc.send(Command{WalletList: args})
}

func (cc *CommandClient) WalletExport(args *WalletExportArgs) {
	cc.send(Command{WalletExport: args})
}

func (cc *CommandClient) WalletPay(args *WalletPayArgs) {
	cc.send(Command{WalletPay: args})
}

func (cc *CommandClient) Commit(args *CommArgs) {
	cc.send(Command{Commit: args})
}

func (cc *CommandClient) Get(args *GetArgs) {
	cc.send(Command{Get: args})
}

func (cc *CommandClient) List(args *ListArgs) {
	cc.send(Command{List: args})
}

func (cc *CommandClient) Import(args *ImportArgs) {
	cc.send(Command{Import: args})
}

func (cc *CommandClient) PaySubmit(args *PayArgs) {
	cc.send(Command{PaySubmit: args})
}

func (cc *CommandClient) PayList(args *PayArgs) {
	cc.send(Command{PayList: args})
}

func (cc *CommandClient) PaySettle(args *PayArgs) {
	cc.send(Command{PaySettle: args})
}

func (cc *CommandClient) PayCollect(args *PayArgs) {
	cc.send(Command{PayCollect: args})
}

func (cc *CommandClient) PayTrack(args *PayArgs) {
	cc.send(Command{PayTrack: args})
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
