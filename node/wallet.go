package node

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/filecoin-project/go-address"
	"github.com/myelnet/pop/wallet"
)

// WalletList returns a list of all addresses for which we have the private keys
func (nd *Pop) WalletList(ctx context.Context, args *WalletListArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			WalletResult: &WalletResult{
				Err: err.Error(),
			},
		})
	}

	addresses, err := nd.exch.Wallet().List()
	if err != nil {
		sendErr(fmt.Errorf("failed to list addresses: %v", err))
		return
	}

	var stringAddresses = make([]string, len(addresses))

	for i, addr := range addresses {
		stringAddresses[i] = addr.String()
	}

	nd.send(Notify{
		WalletResult: &WalletResult{
			Addresses:      stringAddresses,
			DefaultAddress: nd.exch.Wallet().DefaultAddress().String(),
		},
	})
}

// WalletExport writes the private key for a given address to a file at the given path
func (nd *Pop) WalletExport(ctx context.Context, args *WalletExportArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			WalletResult: &WalletResult{
				Err: err.Error(),
			},
		})
	}

	err := nd.exportPrivateKey(ctx, args.Address, args.OutputPath)
	if err != nil {
		sendErr(fmt.Errorf("cannot export private key: %v", err))
		return
	}

	nd.send(Notify{
		WalletResult: &WalletResult{},
	})
}

// WalletPay transfers funds from one given address for which we have the private key to another one
func (nd *Pop) WalletPay(ctx context.Context, args *WalletPayArgs) {
	sendErr := func(err error) {
		nd.send(Notify{
			WalletResult: &WalletResult{
				Err: err.Error(),
			},
		})
	}

	from, err := address.NewFromString(args.From)
	if err != nil {
		sendErr(fmt.Errorf("failed to decode address %s : %v", args.From, err))
		return
	}

	to, err := address.NewFromString(args.To)
	if err != nil {
		sendErr(fmt.Errorf("failed to decode address %s : %v", args.To, err))
		return
	}

	err = nd.exch.Wallet().Transfer(ctx, from, to, args.Amount)
	if err != nil {
		sendErr(err)
		return
	}

	nd.send(Notify{
		WalletResult: &WalletResult{},
	})
}

// importPrivateKey from a hex encoded private key to use as default on the exchange instead of
// the auto generated one. This is mostly for development and will be reworked into a nicer command
// eventually
func (nd *Pop) importPrivateKey(ctx context.Context, pk string) error {
	var iki wallet.KeyInfo

	data, err := hex.DecodeString(pk)
	if err != nil {
		return fmt.Errorf("failed to decode key: %v", err)
	}

	err = iki.FromBytes(data)
	if err != nil {
		return fmt.Errorf("failed to decode keyInfo: %v", err)
	}

	addr, err := nd.exch.Wallet().ImportKey(ctx, &iki)
	if err != nil {
		return fmt.Errorf("failed to import key: %v", err)
	}

	err = nd.exch.Wallet().SetDefaultAddress(addr)
	if err != nil {
		return fmt.Errorf("failed to set default address: %v", err)
	}

	return nil
}

// exportPrivateKey exports the private key of a given address to an output file
func (nd *Pop) exportPrivateKey(ctx context.Context, addr, outputPath string) error {
	adr, err := address.NewFromString(addr)
	if err != nil {
		return fmt.Errorf("failed to decode address: %v", err)
	}

	key, err := nd.exch.Wallet().ExportKey(ctx, adr)
	if err != nil {
		return fmt.Errorf("address %s does not exist", addr)
	}

	data, err := key.ToBytes()
	if err != nil {
		return fmt.Errorf("failed to convert address to bytes: %v", err)
	}

	encodedPk := make([]byte, hex.EncodedLen(len(data)))
	hex.Encode(encodedPk, data)

	err = os.WriteFile(outputPath, encodedPk, 0666)
	if err != nil {
		return fmt.Errorf("failed to export KeyInfo to %s: %v", addr, err)
	}

	return nil
}
