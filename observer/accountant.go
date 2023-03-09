package observer

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"

	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/safe/apps/bitcoin"
	"github.com/MixinNetwork/safe/common"
	"github.com/MixinNetwork/safe/keeper"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
)

func (node *Node) bitcoinAccountantSignTransaction(ctx context.Context, extra []byte) error {
	transactionHash := hex.EncodeToString(extra[:32])
	spsbt, _ := bitcoin.UnmarshalPartiallySignedTransaction(extra[32:])

	tx, err := node.store.ReadTransactionApproval(ctx, transactionHash)
	if err != nil {
		return err
	}
	if tx.State == common.RequestStateDone {
		return nil
	}
	if tx.Chain != keeper.SafeChainBitcoin {
		panic(transactionHash)
	}
	b := common.DecodeHexOrPanic(tx.RawTransaction)
	hpsbt, _ := bitcoin.UnmarshalPartiallySignedTransaction(b)

	requests, err := node.keeperStore.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateDone)
	if err != nil {
		return err
	}
	signed := make(map[int][]byte)
	for _, r := range requests {
		signed[r.InputIndex] = common.DecodeHexOrPanic(r.Signature.String)
	}

	msgTx := spsbt.Packet.UnsignedTx
	for idx := range msgTx.TxIn {
		pop := msgTx.TxIn[idx].PreviousOutPoint
		hash := spsbt.SigHash(idx)
		utxo, _ := node.keeperStore.ReadBitcoinUTXO(ctx, pop.Hash.String(), int(pop.Index))
		required := node.checkBitcoinUTXOSignatureRequired(ctx, pop)
		if required {
			hpin := hpsbt.Packet.Inputs[idx]
			hsig := hpin.PartialSigs[0]
			if hex.EncodeToString(hsig.PubKey) != tx.Holder {
				panic(transactionHash)
			}
			sig := append(hsig.Signature, byte(txscript.SigHashAll))
			msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, []byte{})
			msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, sig)

			spin := spsbt.Packet.Inputs[idx]
			ssig := spin.PartialSigs[0]
			if hex.EncodeToString(ssig.PubKey) != tx.Signer {
				panic(transactionHash)
			}
			if !bytes.Equal(ssig.Signature, signed[idx]) {
				panic(transactionHash)
			}
			der, _ := ecdsa.ParseDERSignature(ssig.Signature)
			pub := common.DecodeHexOrPanic(tx.Signer)
			signer, _ := btcutil.NewAddressPubKey(pub, &chaincfg.MainNetParams)
			if !der.Verify(hash, signer.PubKey()) {
				panic(transactionHash)
			}
			sig = append(ssig.Signature, byte(txscript.SigHashAll))
			msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, sig)
			msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, []byte{1})
			msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, utxo.Script)

			hpsbt.Packet.Inputs[idx].PartialSigs = append(hpin.PartialSigs, spin.PartialSigs...)
		} else {
			accountant, err := node.bitcoinReadAccountantKey(ctx, tx.Accountant)
			if err != nil {
				return err
			}
			signature := ecdsa.Sign(accountant, hash)
			sig := append(signature.Serialize(), byte(txscript.SigHashAll))
			msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, sig)
			msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, utxo.Script)

			hpsbt.Packet.Inputs[idx].PartialSigs = []*psbt.PartialSig{{
				PubKey:    common.DecodeHexOrPanic(tx.Accountant),
				Signature: signature.Serialize(),
			}}
		}
	}

	var signedBuffer bytes.Buffer
	err = msgTx.BtcEncode(&signedBuffer, wire.ProtocolVersion, wire.WitnessEncoding)
	if err != nil {
		panic(err)
	}

	raw := hex.EncodeToString(hpsbt.Marshal())
	err = node.store.FinishTransactionSignatures(ctx, transactionHash, raw)
	logger.Printf("store.FinishTransactionSignatures(%s) => %v", transactionHash, err)
	if err != nil {
		return err
	}
	return node.bitcoinBroadcastTransaction(transactionHash, signedBuffer.Bytes())
}

func (node *Node) bitcoinBroadcastTransaction(hash string, raw []byte) error {
	id, err := bitcoin.RPCSendRawTransaction(node.conf.BitcoinRPC, hex.EncodeToString(raw))
	if err != nil {
		return err
	}
	if id != hash {
		return fmt.Errorf("malformed bitcoin transaction %s %s", hash, id)
	}
	return nil
}