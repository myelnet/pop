package payments

import (
	"bytes"
	"fmt"

	"github.com/filecoin-project/go-address"
	"github.com/filecoin-project/go-state-types/abi"

	"github.com/filecoin-project/specs-actors/v2/actors/builtin"
	init2 "github.com/filecoin-project/specs-actors/v2/actors/builtin/init"
	"github.com/filecoin-project/specs-actors/v2/actors/builtin/paych"

	fil "github.com/myelnet/go-hop-exchange/filecoin"
	cbg "github.com/whyrusleeping/cbor-gen"
)

// MessageBuilder abstracts methods to create new payment channel messages
type MessageBuilder interface {
	Create(to address.Address, initialAmount abi.TokenAmount) (*fil.Message, error)
	Update(paych address.Address, voucher *paych.SignedVoucher, secret []byte) (*fil.Message, error)
	Settle(paych address.Address) (*fil.Message, error)
	Collect(paych address.Address) (*fil.Message, error)
}

type message struct {
	from address.Address
}

func (m message) Create(to address.Address, initialAmount abi.TokenAmount) (*fil.Message, error) {
	params, err := SerializeParams(&paych.ConstructorParams{From: m.from, To: to})
	if err != nil {
		return nil, err
	}
	enc, err := SerializeParams(&init2.ExecParams{
		CodeCID:           builtin.PaymentChannelActorCodeID,
		ConstructorParams: params,
	})
	if err != nil {
		return nil, err
	}

	return &fil.Message{
		To:     builtin.InitActorAddr,
		From:   m.from,
		Value:  initialAmount,
		Method: builtin.MethodsInit.Exec,
		Params: enc,
	}, nil
}

func (m message) Update(ch address.Address, sv *paych.SignedVoucher, secret []byte) (*fil.Message, error) {
	params, aerr := SerializeParams(&paych.UpdateChannelStateParams{
		Sv:     *sv,
		Secret: secret,
	})
	if aerr != nil {
		return nil, aerr
	}

	return &fil.Message{
		To:     ch,
		From:   m.from,
		Value:  abi.NewTokenAmount(0),
		Method: builtin.MethodsPaych.UpdateChannelState,
		Params: params,
	}, nil
}

func (m message) Settle(ch address.Address) (*fil.Message, error) {
	return &fil.Message{
		To:     ch,
		From:   m.from,
		Value:  abi.NewTokenAmount(0),
		Method: builtin.MethodsPaych.Settle,
	}, nil
}

func (m message) Collect(ch address.Address) (*fil.Message, error) {
	return &fil.Message{
		To:     ch,
		From:   m.from,
		Value:  abi.NewTokenAmount(0),
		Method: builtin.MethodsPaych.Collect,
	}, nil
}

func SerializeParams(i cbg.CBORMarshaler) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := i.MarshalCBOR(buf); err != nil {
		return nil, fmt.Errorf("failed to encode parameter")
	}
	return buf.Bytes(), nil
}
