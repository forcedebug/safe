package keeper

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/safe/common"
	"github.com/MixinNetwork/safe/keeper/store"
	"github.com/fox-one/mixin-sdk-go"
)

const (
	SignerKeygenMaximum = 128
)

func (node *Node) sendSignerKeygenRequest(ctx context.Context, req *common.Request) error {
	if req.Role != common.RequestRoleObserver {
		panic(req.Role)
	}
	if req.Action != common.ActionObserverRequestSignerKeys {
		panic(req.Action)
	}
	switch req.Curve {
	case common.CurveSecp256k1ECDSABitcoin:
	default:
		return node.store.FinishRequest(ctx, req.Id)
	}

	batch, ok := new(big.Int).SetString(req.Extra, 16)
	if !ok || batch.Cmp(big.NewInt(1)) < 0 || batch.Cmp(big.NewInt(SignerKeygenMaximum)) > 0 {
		return node.store.FinishRequest(ctx, req.Id)
	}
	for i := 0; i < int(batch.Int64()); i++ {
		op := &common.Operation{
			Type:  common.OperationTypeKeygenInput,
			Curve: req.Curve,
		}
		op.Id = mixin.UniqueConversationID(req.Id, fmt.Sprintf("%8d", i))
		op.Id = mixin.UniqueConversationID(op.Id, fmt.Sprintf("MTG:%v:%d", node.signer.Genesis.Members, node.signer.Genesis.Threshold))
		err := node.buildSignerTransaction(ctx, op)
		if err != nil {
			return err
		}
	}

	return node.store.FinishRequest(ctx, req.Id)
}

func (node *Node) sendSignerSignRequest(ctx context.Context, req *store.SignatureRequest) error {
	switch req.Curve {
	case common.CurveSecp256k1ECDSABitcoin:
	default:
		panic(req.Curve)
	}

	op := &common.Operation{
		Id:     req.RequestId,
		Type:   common.OperationTypeSignInput,
		Curve:  req.Curve,
		Public: hex.EncodeToString(common.ShortSum(req.Signer)),
		Extra:  common.DecodeHexOrPanic(req.Message),
	}
	return node.buildSignerTransaction(ctx, op)
}

func (node *Node) encryptSignerOperation(op *common.Operation) []byte {
	extra := op.Encode()
	return common.AESEncrypt(node.signerAESKey[:], extra, op.Id)
}

func (node *Node) buildSignerTransaction(ctx context.Context, op *common.Operation) error {
	extra := node.encryptSignerOperation(op)
	if len(extra) > 160 {
		panic(fmt.Errorf("node.buildSignerTransaction(%v) omitted %x", op, extra))
	}
	members := node.signer.Genesis.Members
	threshold := node.signer.Genesis.Threshold
	err := node.buildTransaction(ctx, node.conf.AssetId, members, threshold, "1", extra, op.Id)
	logger.Printf("node.buildSignerTransaction(%v) => %s %x %v", op, op.Id, extra, err)
	return err
}