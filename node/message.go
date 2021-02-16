package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/rs/zerolog/log"
)

var jsonEscapedZero = []byte(`\u0000`)

// PingArgs get passed to the Ping command
type PingArgs struct {
	IP string
}

// AddArgs get passed to the Add command
type AddArgs struct {
	Path string
}

// GetArgs get passed to the Get command
type GetArgs struct {
	Cid     string
	Sel     string
	Out     string
	Timeout int
}

// Command is a message sent from a client to the daemon
type Command struct {
	Ping *PingArgs
	Add  *AddArgs
	Get  *GetArgs
}

// PingResult is sent in the notify message to give us the info we requested
type PingResult struct {
	ListenAddr string
}

// AddResult gives us feedback on the result of the Add request
type AddResult struct {
	Cid string
	Err string
}

// GetResult gives us feedback on the result of the Get request
type GetResult struct {
	DealID     string
	TotalSpent string
	Err        string
}

// Notify is a message sent from the daemon to the client
type Notify struct {
	PingResult *PingResult
	AddResult  *AddResult
	GetResult  *GetResult
}

// CommandServer receives commands on the daemon side and executes them
type CommandServer struct {
	n             IPFSNode             // the ipfs node we are controlling
	sendNotifyMsg func(jsonMsg []byte) // send a notification message
}

func NewCommandServer(ipfs IPFSNode, sendNotifyMsg func(b []byte)) *CommandServer {
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
		cs.n.Ping(c.IP)
		return nil
	}
	if c := cmd.Add; c != nil {
		cs.n.Add(ctx, c)
		return nil
	}
	if c := cmd.Get; c != nil {
		cs.n.Get(ctx, c)
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

func (cc *CommandClient) Ping(ip string) {
	cc.send(Command{Ping: &PingArgs{IP: ip}})
}

func (cc *CommandClient) Add(args *AddArgs) {
	cc.send(Command{Add: args})
}

func (cc *CommandClient) Get(args *GetArgs) {
	cc.send(Command{Get: args})
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
		return nil, fmt.Errorf("ReadMsg: message too large: %v bytes", n)
	}
	b := make([]byte, n)
	nn, err := io.ReadFull(r, b)
	if err != nil {
		return nil, err
	}
	if nn != int(n) {
		return nil, fmt.Errorf("ReadMsg: expected %v bytes, got %v", n, nn)
	}
	return b, nil
}

func WriteMsg(w io.Writer, b []byte) error {

	cb := make([]byte, 4)
	if len(b) > MaxMessageSize {
		return fmt.Errorf("WriteMsg: message too large: %v bytes", len(b))
	}
	binary.LittleEndian.PutUint32(cb, uint32(len(b)))
	n, err := w.Write(cb)
	if err != nil {
		return err
	}
	if n != 4 {
		return fmt.Errorf("WriteMsg: short write: %v bytes (wanted 4)", n)
	}
	n, err = w.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return fmt.Errorf("WriteMsg: short write: %v bytes (wanted %v)", n, len(b))
	}
	return nil
}
