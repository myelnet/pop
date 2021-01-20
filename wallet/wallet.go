package wallet

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/crypto"
	"github.com/ipfs/go-ipfs/keystore"
	ci "github.com/libp2p/go-libp2p-core/crypto"
	pb "github.com/libp2p/go-libp2p-core/crypto/pb"
)

const (
	KNamePrefix = "wallet-"
	KDefault    = "default"
)

// KeyType enumerates all the types of key we support
type KeyType string

// Only supporting secp for now since bls cannot be stored in ipfs keystore
const (
	KTSecp256k1 KeyType = "secp256k1"
	KTBLS       KeyType = "bls"
)

// KeyInfo stores info about a private key
type KeyInfo struct {
	KType      KeyType `json:"Type"` // Had to name it KType as Type() is used already
	PrivateKey []byte
	//internal signer
	sig Signer
}

// Key adds the public key and address on top of the private key
type Key struct {
	KeyInfo

	PublicKey []byte
	Address   address.Address
}

// Bytes implements libp2p PrivKey interface for IPFS keystore compat
// do not use
func (k *Key) Bytes() ([]byte, error) {
	return k.PrivateKey, nil
}

// Raw implements libp2p Key interface for IPFS keystore compat
func (k *Key) Raw() ([]byte, error) {
	return k.PrivateKey, nil
}

// Equals implements libp2p key interface for IPFS keystore compat
func (k *Key) Equals(ki ci.Key) bool {
	pki, _ := ki.Raw()
	return bytes.Equal(k.PrivateKey, pki)
}

// Type implements libp2p key interface for IPFS keystore compat
func (k *Key) Type() pb.KeyType {
	switch k.KType {
	case KTSecp256k1:
		return pb.KeyType_Secp256k1
	case KTBLS:
		return pb.KeyType(4) // 4 doesnt' exist in libp2p
	default:
		// No unknown so we fall back to RSA as our unknown
		return pb.KeyType_RSA
	}
}

// Sign implements libp2p PrivKey interface for IPFS keystore compat
func (k *Key) Sign(data []byte) ([]byte, error) {
	return k.sig.Sign(k.PrivateKey, data)
}

// GetPublic implements libp2p PrivKey interface for IPFS keystore compat
func (k *Key) GetPublic() ci.PubKey {
	return k
}

// Verify implements libp2p PubKey interface for IPFS keystore compat
func (k *Key) Verify(data []byte, sig []byte) (bool, error) {
	err := k.sig.Verify(sig, k.Address, data)
	if err != nil {
		return false, err
	}
	return true, nil
}

// NewKeyFromKeyInfo adds public key and address to private key
func NewKeyFromKeyInfo(ki KeyInfo) (*Key, error) {
	var err error
	k := &Key{
		KeyInfo: ki,
	}
	k.PublicKey, err = ki.sig.ToPublic(ki.PrivateKey)
	if err != nil {
		return nil, err
	}

	switch ki.KType {
	case KTSecp256k1:
		k.Address, err = address.NewSecp256k1Address(k.PublicKey)
		if err != nil {
			return nil, err
		}
	case KTBLS:
		k.Address, err = address.NewBLSAddress(k.PublicKey)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("key type not supported")
	}
	return k, nil
}

// NewKeyFromLibp2p converts a libp2p crypto private key interface into a Key
func NewKeyFromLibp2p(pk ci.PrivKey) (*Key, error) {
	var tp KeyType
	switch pk.Type() {
	case pb.KeyType_Secp256k1:
		tp = KTSecp256k1
	case 4:
		tp = KTBLS
	default:
		return nil, fmt.Errorf("key type not supported")
	}
	raw, err := pk.Raw()
	if err != nil {
		return nil, err
	}
	// Hopefully we don't get an error?
	ki := KeyInfo{
		KType:      tp,
		PrivateKey: raw,
	}
	return NewKeyFromKeyInfo(ki)
}

// Signer encapsulates methods for a given key
type Signer interface {
	GenPrivate() ([]byte, error)
	ToPublic(pk []byte) ([]byte, error)
	Sign(pk []byte, msg []byte) ([]byte, error)
	Verify(sig []byte, a address.Address, msg []byte) error
}

//Driver is a lightweight interface to control any available keychain and interact with blockchains
type Driver interface {
	NewKey(context.Context, KeyType) (address.Address, error)
	ImportKey(context.Context, *KeyInfo) (address.Address, error)
	Sign(context.Context, address.Address, []byte) (*crypto.Signature, error)
	Verify(context.Context, address.Address, []byte, *crypto.Signature) (bool, error)
}

// IPFS wallet wraps an IPFS keystore
type IPFS struct {
	keystore keystore.Keystore
	keys     map[address.Address]*Key // cache so we don't read from the Keystore too much

	lk sync.Mutex
}

// NewIPFS creates a new IPFS keystore based wallet implementing the Driver methods
func NewIPFS(ks keystore.Keystore) Driver {
	return &IPFS{
		keystore: ks,
		keys:     make(map[address.Address]*Key),
	}
}

// NewKey generates a brand new key for the given type in our wallet and returns the address
func (i *IPFS) NewKey(ctx context.Context, kt KeyType) (address.Address, error) {
	i.lk.Lock()
	defer i.lk.Unlock()

	sig, err := KeyTypeSig(kt)
	if err != nil {
		return address.Undef, err
	}
	pk, err := sig.GenPrivate()
	if err != nil {
		return address.Undef, err
	}
	ki := KeyInfo{
		KType:      kt,
		PrivateKey: pk,
		sig:        sig,
	}

	k, err := NewKeyFromKeyInfo(ki)
	if err != nil {
		return address.Undef, err
	}

	if err := i.keystore.Put(KNamePrefix+k.Address.String(), k); err != nil {
		return address.Undef, fmt.Errorf("unable to save to keystore: %v", err)
	}
	i.keys[k.Address] = k

	_, err = i.keystore.Get(KDefault)
	if err != nil {
		if err != keystore.ErrNoSuchKey {
			return address.Undef, err
		}
		if err := i.keystore.Put(KDefault, k); err != nil {
			return address.Undef, fmt.Errorf("failed to set new key as default: %v", err)
		}
	}
	return k.Address, nil
}

// ImportKey in the wallet from a private key and key type
func (i *IPFS) ImportKey(ctx context.Context, k *KeyInfo) (address.Address, error) {
	sig, err := KeyTypeSig(k.KType)
	if err != nil {
		return address.Undef, err
	}
	k.sig = sig
	key, err := NewKeyFromKeyInfo(*k)
	if err != nil {
		return address.Undef, err
	}
	if err := i.keystore.Put(KNamePrefix+key.Address.String(), key); err != nil {
		return address.Undef, err
	}
	i.keys[key.Address] = key
	return key.Address, nil
}

// Sign a message with the key associated with the given address. Generates a valid Filecoin signature
func (i *IPFS) Sign(ctx context.Context, addr address.Address, msg []byte) (*crypto.Signature, error) {
	k, err := i.getKey(addr)
	if err != nil {
		return nil, err
	}
	sigType := ActSigType(k.KType)
	s, err := k.sig.Sign(k.PrivateKey, msg)
	if err != nil {
		return nil, err
	}
	return &crypto.Signature{
		Type: sigType,
		Data: s,
	}, nil
}

func (i *IPFS) Verify(ctx context.Context, k address.Address, msg []byte, sig *crypto.Signature) (bool, error) {
	signer, err := SigTypeSig(sig.Type)
	if err != nil {
		return false, err
	}
	err = signer.Verify(sig.Data, k, msg)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (i *IPFS) getKey(addr address.Address) (*Key, error) {
	i.lk.Lock()
	defer i.lk.Unlock()

	k, ok := i.keys[addr]
	if ok {
		return k, nil
	}
	ki, err := i.keystore.Get(KNamePrefix + addr.String())
	if err != nil {
		return nil, err
	}

	k, err = NewKeyFromLibp2p(ki)
	if err != nil {
		return nil, err
	}

	i.keys[k.Address] = k
	return k, nil
}

// KeyTypeSig selects the signer based on key type
func KeyTypeSig(typ KeyType) (Signer, error) {
	switch typ {
	case KTSecp256k1:
		return secp{}, nil
	case KTBLS:
		return bls{}, nil
	default:
		return nil, fmt.Errorf("key type not supported")
	}
}

// SigTypeSig selects the signer based on sig type
func SigTypeSig(st crypto.SigType) (Signer, error) {
	switch st {
	case crypto.SigTypeSecp256k1:
		return secp{}, nil
	case crypto.SigTypeBLS:
		return bls{}, nil
	default:
		return nil, fmt.Errorf("sig type not supported")
	}
}

// ActSigType converts a key type to a Filecoin signature type
func ActSigType(typ KeyType) crypto.SigType {
	switch typ {
	case KTBLS:
		return crypto.SigTypeBLS
	case KTSecp256k1:
		return crypto.SigTypeSecp256k1
	default:
		return crypto.SigTypeUnknown
	}
}
