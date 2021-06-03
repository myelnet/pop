package node

import (
	"bufio"
	"context"

	"github.com/filecoin-project/go-commp-utils/writer"
	"github.com/filecoin-project/go-state-types/abi"
	"github.com/ipfs/go-blockservice"
	"github.com/ipfs/go-cid"
	offline "github.com/ipfs/go-ipfs-exchange-offline"
	"github.com/ipfs/go-merkledag"
	"github.com/ipld/go-car"
)

//go:generate cbor-gen-for --map-encoding CommitRef

// CommitRef is a CBOR encoded struct referencing a transaction root
type CommitRef struct {
	RootCID cid.Cid
}

// PieceRef contains Filecoin metadata about a storage piece
type PieceRef struct {
	CID         cid.Cid
	PayloadSize int64
	PieceSize   abi.PaddedPieceSize
}

// archive a DAG into a CAR
func (nd *node) archive(ctx context.Context, root cid.Cid) (*PieceRef, error) {
	ref := CommitRef{root}
	if err := nd.pieceHAMT.Set(ctx, root.String(), &ref); err != nil {
		return nil, err
	}
	if err := nd.pieceHAMT.Flush(ctx); err != nil {
		return nil, err
	}
	proot, err := nd.is.Put(ctx, nd.pieceHAMT)
	if err != nil {
		return nil, err
	}

	bs := nd.ms.MultiReadBlockstore()
	dag := merkledag.NewDAGService(blockservice.New(bs, offline.Exchange(bs)))

	wr := &writer.Writer{}
	bw := bufio.NewWriterSize(wr, int(writer.CommPBuf))

	err = car.WriteCar(ctx, dag, []cid.Cid{proot}, wr)
	if err != nil {
		return nil, err
	}

	if err := bw.Flush(); err != nil {
		return nil, err
	}

	dataCIDSize, err := wr.Sum()
	if err != nil {
		return nil, err
	}

	return &PieceRef{
		CID:         dataCIDSize.PieceCID,
		PayloadSize: dataCIDSize.PayloadSize,
		PieceSize:   dataCIDSize.PieceSize,
	}, nil
}
