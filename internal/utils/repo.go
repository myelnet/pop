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

	"github.com/filecoin-project/go-hamt-ipld/v3"
	blocks "github.com/ipfs/go-block-format"
	cid "github.com/ipfs/go-cid"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	keystore "github.com/ipfs/go-ipfs-keystore"
	ci "github.com/libp2p/go-libp2p-core/crypto"
	"github.com/libp2p/go-libp2p-core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// Dumping a bunch of shared stuff here. We can reorganize once we have a clearer idea
// of what all the different components look like

// KLibp2pHost is the datastore key for storing our libp2p identity private key
const KLibp2pHost = "libp2p-host"

// RepoPath is akin to IPFS: ~/.pop by default or changed via $POP_PATH
func RepoPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".pop"), nil
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

// StringsToPeerIDs parses a list of strings into peer IDs
func StringsToPeerIDs(strIDs []string) ([]peer.ID, error) {
	var peers []peer.ID
	for _, s := range strIDs {
		if s == "" {
			continue
		}
		pid, err := peer.Decode(s)
		if err != nil {
			return peers, fmt.Errorf("failed to decode peer %s: %w", s, err)
		}
		peers = append(peers, pid)
	}
	return peers, nil
}

func NewBlockstoreWrapper(bs blockstore.Blockstore) *BlockstoreWrapper {
	return &BlockstoreWrapper{bs}
}

// BlockstoreWrapper is a crutch until specs-actors upgrade to latest blockstore
type BlockstoreWrapper struct {
	bs blockstore.Blockstore
}

func (bw *BlockstoreWrapper) Get(key cid.Cid) (blocks.Block, error) {
	return bw.bs.Get(context.TODO(), key)
}

func (bw *BlockstoreWrapper) Put(blk blocks.Block) error {
	return bw.bs.Put(context.TODO(), blk)
}

func (bw *BlockstoreWrapper) Has(key cid.Cid) (bool, error) {
	return bw.bs.Has(context.TODO(), key)
}
