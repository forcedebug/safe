package keeper

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MixinNetwork/mixin/crypto"
	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/safe/apps/bitcoin"
	"github.com/MixinNetwork/safe/apps/ethereum"
	"github.com/MixinNetwork/safe/common"
	"github.com/MixinNetwork/safe/common/abi"
	"github.com/MixinNetwork/safe/signer"
	"github.com/MixinNetwork/trusted-group/mtg"
	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/fox-one/mixin-sdk-go/v2"
	"github.com/gofrs/uuid/v5"
	"github.com/pelletier/go-toml"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

const (
	testAccountPriceAssetId          = "31d2ea9c-95eb-3355-b65b-ba096853bc18"
	testAccountPriceAmount           = 3.0123
	testBondAssetId                  = "d25a6ea6-f4d7-3100-8425-29f8bb82ac2f"
	testSafeBondReceiverId           = "e459de8b-4edd-44ff-a119-b1d707f8521a"
	testBitcoinKeyHolderPrivate      = "52250bb9b9edc5d54466182778a6470a5ee34033c215c92dd250b9c2ce543556"
	testBitcoinKeyObserverPrivate    = "35fe01cbdc659810854615319b51899b78966c513f0515ee9d77ef6016090221"
	testBitcoinKeyObserverChainCode  = "0619f13c84e1d2bfd6f20ca75a03bee058a95024338c583e1aa8761348dbb249"
	testBitcoinKeyAccountantPriate   = "c663c88aab70d1539b22f475cb8febc714dc61b9a43b472dc1ef970786cf31f9"
	testBitcoinKeyDummyHolderPrivate = "75d5f311c8647e3a1d84a0d975b6e50b8c6d3d7f195365320077f41c6a165155"
	testSafeAddress                  = "bc1qm7qaucdjwzpapugfvmzp2xduzs7p0jd3zq7yxpvuf9dp5nml3pesx57a9x"
	testTransactionReceiver          = "bc1ql0up0wwazxt6xlj84u9fnvhnagjjetcn7h4z5xxvd0kf5xuczjgqq2aehc"
	testTimelockDuration             = bitcoin.TimeLockMinimum

	testHolderSigner   = 0
	testSignerObserver = 1
	testHolderObserver = 2
)

var sequence uint64 = 5000000

func TestBitcoinKeeper(t *testing.T) {
	require := require.New(t)
	ctx, node, db, mpc, signers := testPrepare(require)

	observer := testPublicKey(testBitcoinKeyObserverPrivate)
	bondId := testDeployBondContract(ctx, require, node, testSafeAddress, common.SafeBitcoinChainId)
	require.Equal(testBondAssetId, bondId)
	output, err := testWriteOutput(ctx, db, node.conf.AppId, bondId, testGenerateDummyExtra(node), sequence, decimal.NewFromInt(1000000))
	require.Nil(err)
	node.ProcessOutput(ctx, &mtg.Action{
		UnifiedOutput: *output,
	})
	input := &bitcoin.Input{
		TransactionHash: "40e228e5a3cba99fd3fc5350a00bfeef8bafb760e26919ec74bca67776c90427",
		Index:           0, Satoshi: 86560,
	}
	testObserverHolderDeposit(ctx, require, node, mpc, observer, input, 1)
	input = &bitcoin.Input{
		TransactionHash: "851ce979f17df66d16be405836113e782512159b4bb5805e5385cdcbf1d45194",
		Index:           0, Satoshi: 100000,
	}
	testObserverHolderDeposit(ctx, require, node, mpc, observer, input, 2)

	holder := testPublicKey(testBitcoinKeyHolderPrivate)
	outputs, err := node.store.ListAllBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(outputs, 2)
	pendings, err := node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 0)

	transactionHash := testSafeProposeTransaction(ctx, require, node, bondId, "3e37ea1c-1455-400d-9642-f6bbcd8c744e", "18d6e8a1bcce1b1dddbfed5826cde933dc55ba65a733fc5a2198f113c86e31d0", "70736274ff0100cd02000000022704c97677a6bc74ec1969e260b7af8beffe0ba05053fcd39fa9cba3e528e2400000000000ffffffff9451d4f1cbcd85535e80b54b9b151225783e11365840be166df67df179e91c850000000000ffffffff030c30000000000000220020fbf817b9dd1197a37e47af0a99b2f3ea252caf13f5ea2a18cc6bec9a1b981490b4a8020000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f88730000000000000000126a103e37ea1c1455400d9642f6bbcd8c744e000000000001012b2052010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b292689352870001012ba086010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b2926893528700000000")
	outputs, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(outputs, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 2)
	testSafeRevokeTransaction(ctx, require, node, transactionHash, false)
	outputs, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(outputs, 2)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 0)

	transactionHash = testSafeProposeTransaction(ctx, require, node, bondId, "8bf052c1-41f4-4547-8091-bcf0c85f09a6", "29091ae146a7ab2e547d0e3fddd21f65a8de4314d66ffc2c4c11f3fa444ea706", "70736274ff0100cd02000000022704c97677a6bc74ec1969e260b7af8beffe0ba05053fcd39fa9cba3e528e2400000000000ffffffff9451d4f1cbcd85535e80b54b9b151225783e11365840be166df67df179e91c850000000000ffffffff030c30000000000000220020fbf817b9dd1197a37e47af0a99b2f3ea252caf13f5ea2a18cc6bec9a1b981490b4a8020000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f88730000000000000000126a108bf052c141f445478091bcf0c85f09a6000000000001012b2052010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b292689352870001012ba086010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b2926893528700000000")
	outputs, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(outputs, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 2)
	testSafeRevokeTransaction(ctx, require, node, transactionHash, true)
	outputs, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(outputs, 2)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 0)

	transactionHash = testSafeProposeTransaction(ctx, require, node, bondId, "b0a22078-0a86-459d-93f4-a1aadbf2b9b7", "5f489b710d495808d7693f0d1b62b6af05d0af69b52980d3e4263c66dde9e676", "70736274ff0100cd02000000022704c97677a6bc74ec1969e260b7af8beffe0ba05053fcd39fa9cba3e528e2400000000000ffffffff9451d4f1cbcd85535e80b54b9b151225783e11365840be166df67df179e91c850000000000ffffffff030c30000000000000220020fbf817b9dd1197a37e47af0a99b2f3ea252caf13f5ea2a18cc6bec9a1b981490b4a8020000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f88730000000000000000126a10b0a220780a86459d93f4a1aadbf2b9b7000000000001012b2052010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b292689352870001012ba086010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b2926893528700000000")
	outputs, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(outputs, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 2)
	signedRaw := testSafeApproveTransaction(ctx, require, node, transactionHash, signers)
	require.Nil(err)
	require.Len(outputs, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 0)
	testSpareKeys(ctx, require, node, 0, 0, 0, common.CurveSecp256k1ECDSABitcoin)

	testAccountantSpentTransaction(ctx, require, signedRaw, testHolderSigner)
}

func TestBitcoinKeeperCloseAccountWithSignerObserver(t *testing.T) {
	require := require.New(t)
	ctx, node, db, mpc, signers := testPrepare(require)

	observer := testPublicKey(testBitcoinKeyObserverPrivate)
	bondId := testDeployBondContract(ctx, require, node, testSafeAddress, common.SafeBitcoinChainId)
	require.Equal(testBondAssetId, bondId)
	output, err := testWriteOutput(ctx, db, node.conf.AppId, bondId, testGenerateDummyExtra(node), sequence, decimal.NewFromInt(1000000))
	require.Nil(err)
	action := &mtg.Action{
		UnifiedOutput: *output,
	}
	node.ProcessOutput(ctx, action)
	input := &bitcoin.Input{
		TransactionHash: "851ce979f17df66d16be405836113e782512159b4bb5805e5385cdcbf1d45194",
		Index:           0,
		Satoshi:         100000,
	}
	testObserverHolderDeposit(ctx, require, node, mpc, observer, input, 1)

	public := testPublicKey(testBitcoinKeyHolderPrivate)
	safe, _ := node.store.ReadSafe(ctx, public)
	require.Equal(common.RequestStateDone, int(safe.State))
	utxos, err := node.store.ListAllBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(utxos, 1)
	pendings, err := node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(pendings, 0)

	transactionHash := testSafeProposeRecoveryTransaction(ctx, require, node, bondId, "3e37ea1c-1455-400d-9642-f6bbcd8c744e", "cbddccdd13631eb68a1d65ace28abd547f62a0937d093d7ba4d0e97f6d86955e", "70736274ff01007902000000019451d4f1cbcd85535e80b54b9b151225783e11365840be166df67df179e91c8500000000000600000002a086010000000000220020fbf817b9dd1197a37e47af0a99b2f3ea252caf13f5ea2a18cc6bec9a1b9814900000000000000000126a103e37ea1c1455400d9642f6bbcd8c744e000000000001012ba086010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b29268935287000000")
	utxos, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(utxos, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(pendings, 1)

	signedRaw := testSafeCloseAccount(ctx, require, node, public, transactionHash, "", signers, action)
	utxos, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(utxos, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(pendings, 0)

	testSpareKeys(ctx, require, node, 0, 0, 0, common.CurveSecp256k1ECDSABitcoin)

	testAccountantSpentTransaction(ctx, require, signedRaw, testSignerObserver)

	tx, _ := node.store.ReadTransaction(ctx, transactionHash)
	safe, _ = node.store.ReadSafe(ctx, tx.Holder)
	require.Equal(common.RequestStateFailed, int(safe.State))
	utxos, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(utxos, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(pendings, 0)
}

func TestBitcoinKeeperCloseAccountWithHolderObserver(t *testing.T) {
	require := require.New(t)
	ctx, node, db, mpc, signers := testPrepare(require)

	observer := testPublicKey(testBitcoinKeyObserverPrivate)
	bondId := testDeployBondContract(ctx, require, node, testSafeAddress, common.SafeBitcoinChainId)
	require.Equal(testBondAssetId, bondId)
	output, err := testWriteOutput(ctx, db, node.conf.AppId, bondId, testGenerateDummyExtra(node), sequence, decimal.NewFromInt(1000000))
	require.Nil(err)
	action := &mtg.Action{
		UnifiedOutput: *output,
	}
	node.ProcessOutput(ctx, action)
	input := &bitcoin.Input{
		TransactionHash: "851ce979f17df66d16be405836113e782512159b4bb5805e5385cdcbf1d45194",
		Index:           0,
		Satoshi:         100000,
	}
	testObserverHolderDeposit(ctx, require, node, mpc, observer, input, 1)

	public := testPublicKey(testBitcoinKeyHolderPrivate)
	safe, _ := node.store.ReadSafe(ctx, public)
	require.Equal(common.RequestStateDone, int(safe.State))
	utxos, err := node.store.ListAllBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(utxos, 1)
	pendings, err := node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(pendings, 0)

	holderSignedRaw := testHolderApproveTransaction("70736274ff01007902000000019451d4f1cbcd85535e80b54b9b151225783e11365840be166df67df179e91c8500000000000600000002a086010000000000220020fbf817b9dd1197a37e47af0a99b2f3ea252caf13f5ea2a18cc6bec9a1b9814900000000000000000126a103e37ea1c1455400d9642f6bbcd8c744e000000000001012ba086010000000000220020df81de61b27083d0f10966c41519bc143c17c9b1103c43059c495a1a4f7f8873010304810000000105762103911c1ef3960be7304596cfa6073b1d65ad43b421a4c272142cc7a8369b510c56ac7c2102339baf159c94cc116562d609097ff3c3bd340a34b9f7d50cc22b8d520301a7c9ac937c829263210333870af2985a674f28bb12290bb0eb403987c2211d9f26267cc4d45ae6797e7cad56b29268935287000000")
	signedRaw := testSafeCloseAccount(ctx, require, node, public, "", holderSignedRaw, signers, action)
	utxos, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(utxos, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(pendings, 0)

	testSpareKeys(ctx, require, node, 0, 0, 0, common.CurveSecp256k1ECDSABitcoin)

	testAccountantSpentTransaction(ctx, require, signedRaw, testHolderObserver)

	safe, _ = node.store.ReadSafe(ctx, public)
	require.Equal(common.RequestStateFailed, int(safe.State))
	utxos, err = node.store.ListAllBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(utxos, 0)
	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	require.Len(pendings, 0)
}

func testPrepare(require *require.Assertions) (context.Context, *Node, *mtg.SQLite3Store, string, []*signer.Node) {
	logger.SetLevel(logger.INFO)
	ctx, signers, _ := signer.TestPrepare(require)
	mpc, cc := signer.TestCMPPrepareKeys(ctx, require, signers, common.CurveSecp256k1ECDSABitcoin)
	chainCode := common.DecodeHexOrPanic(cc)

	root, err := os.MkdirTemp("", "safe-keeper-test-")
	require.Nil(err)
	node, db := testBuildNode(ctx, require, root)
	require.NotNil(node)
	timestamp, err := node.timestamp(ctx)
	require.Nil(err)
	require.Equal(node.conf.MTG.Genesis.Epoch, timestamp)
	testSpareKeys(ctx, require, node, 0, 0, 0, common.CurveSecp256k1ECDSABitcoin)

	id := uuid.Must(uuid.NewV4()).String()
	extra := append([]byte{common.RequestRoleSigner}, chainCode...)
	extra = append(extra, common.RequestFlagNone)
	out := testBuildSignerOutput(node, id, mpc, common.OperationTypeKeygenOutput, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	v, err := node.store.ReadProperty(ctx, id)
	require.Nil(err)
	require.Equal("", v)
	testSpareKeys(ctx, require, node, 0, 1, 0, common.CurveSecp256k1ECDSABitcoin)

	id = uuid.Must(uuid.NewV4()).String()
	observer := testPublicKey(testBitcoinKeyObserverPrivate)
	occ := common.DecodeHexOrPanic(testBitcoinKeyObserverChainCode)
	extra = append([]byte{common.RequestRoleObserver}, occ...)
	extra = append(extra, common.RequestFlagNone)
	out = testBuildObserverRequest(node, id, observer, common.ActionObserverAddKey, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	v, err = node.store.ReadProperty(ctx, id)
	require.Nil(err)
	require.Equal("", v)
	testSpareKeys(ctx, require, node, 0, 1, 1, common.CurveSecp256k1ECDSABitcoin)

	batch := byte(64)
	id = uuid.Must(uuid.NewV4()).String()
	dummy := testPublicKey(testBitcoinKeyDummyHolderPrivate)
	out = testBuildObserverRequest(node, id, dummy, common.ActionObserverRequestSignerKeys, []byte{batch}, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	signerMembers := node.GetSigners()
	for i := byte(0); i < batch; i++ {
		pid := common.UniqueId(id, fmt.Sprintf("%8d", i))
		pid = common.UniqueId(pid, fmt.Sprintf("MTG:%v:%d", signerMembers, node.signer.Genesis.Threshold))
		v, err := node.store.ReadProperty(ctx, pid)
		require.Nil(err)
		var om map[string]any
		err = json.Unmarshal([]byte(v), &om)
		require.Nil(err)
		b, _ := hex.DecodeString(om["memo"].(string))
		b = common.AESDecrypt(node.signerAESKey[:], b)
		o, err := common.DecodeOperation(b)
		require.Nil(err)
		require.Equal(pid, o.Id)
	}
	testSpareKeys(ctx, require, node, 0, 1, 1, common.CurveSecp256k1ECDSABitcoin)

	for i := 0; i < 10; i++ {
		testUpdateAccountPrice(ctx, require, node)
	}
	rid, publicKey := testSafeProposeAccount(ctx, require, node, mpc, observer)
	testSpareKeys(ctx, require, node, 0, 0, 0, common.CurveSecp256k1ECDSABitcoin)
	testSafeApproveAccount(ctx, require, node, mpc, observer, rid, publicKey)
	testSpareKeys(ctx, require, node, 0, 0, 0, common.CurveSecp256k1ECDSABitcoin)
	for i := 0; i < 10; i++ {
		testUpdateNetworkStatus(ctx, require, node, 793574, "00000000000000000002a4f5cd899ea457314c808897c5c5f1f1cd6ffe2b266a")
	}

	return ctx, node, db, mpc, signers
}

func testUpdateAccountPrice(ctx context.Context, require *require.Assertions, node *Node) {
	id := uuid.Must(uuid.NewV4()).String()

	extra := []byte{common.SafeChainBitcoin}
	extra = append(extra, uuid.Must(uuid.FromString(testAccountPriceAssetId)).Bytes()...)
	extra = binary.BigEndian.AppendUint64(extra, testAccountPriceAmount*100000000)
	extra = binary.BigEndian.AppendUint64(extra, 10000)
	dummy := testPublicKey(testBitcoinKeyDummyHolderPrivate)
	out := testBuildObserverRequest(node, id, dummy, common.ActionObserverSetOperationParams, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)

	plan, err := node.store.ReadLatestOperationParams(ctx, common.SafeChainBitcoin, time.Now())
	require.Nil(err)
	require.Equal(testAccountPriceAssetId, plan.OperationPriceAsset)
	require.Equal(fmt.Sprint(testAccountPriceAmount), plan.OperationPriceAmount.String())
	require.Equal("0.0001", plan.TransactionMinimum.String())
}

func testUpdateNetworkStatus(ctx context.Context, require *require.Assertions, node *Node, blockHeight int, blockHash string) {
	id := uuid.Must(uuid.NewV4()).String()
	fee, height := bitcoinMinimumFeeRate, uint64(blockHeight)
	hash, _ := crypto.HashFromString(blockHash)

	extra := []byte{common.SafeChainBitcoin}
	extra = binary.BigEndian.AppendUint64(extra, uint64(fee))
	extra = binary.BigEndian.AppendUint64(extra, height)
	extra = append(extra, hash[:]...)
	dummy := testPublicKey(testBitcoinKeyDummyHolderPrivate)
	out := testBuildObserverRequest(node, id, dummy, common.ActionObserverUpdateNetworkStatus, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)

	info, err := node.store.ReadLatestNetworkInfo(ctx, common.SafeChainBitcoin, time.Now())
	require.Nil(err)
	require.NotNil(info)
	require.Equal(byte(common.SafeChainBitcoin), info.Chain)
	require.Equal(uint64(fee), info.Fee)
	require.Equal(height, info.Height)
	require.Equal(hash.String(), info.Hash)
}

func testSafeRevokeTransaction(ctx context.Context, require *require.Assertions, node *Node, transactionHash string, signByObserver bool) {
	id := uuid.Must(uuid.NewV4()).String()

	tx, _ := node.store.ReadTransaction(ctx, transactionHash)
	require.Equal(common.RequestStateInitial, tx.State)

	var sig []byte
	ms := fmt.Sprintf("REVOKE:%s:%s", tx.RequestId, tx.TransactionHash)
	msg := bitcoin.HashMessageForSignature(ms, common.SafeChainBitcoin)
	if signByObserver {
		observer := testGetDerivedObserverPrivate(require)
		sig = ecdsa.Sign(observer, msg).Serialize()
	} else {
		hb, _ := hex.DecodeString(testBitcoinKeyHolderPrivate)
		holder, _ := btcec.PrivKeyFromBytes(hb)
		sig = ecdsa.Sign(holder, msg).Serialize()
	}
	extra := uuid.Must(uuid.FromString(tx.RequestId)).Bytes()
	extra = append(extra, sig...)

	out := testBuildObserverRequest(node, id, testPublicKey(testBitcoinKeyHolderPrivate), common.ActionBitcoinSafeRevokeTransaction, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	requests, err := node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateInitial)
	require.Nil(err)
	require.Len(requests, 0)
	tx, _ = node.store.ReadTransaction(ctx, transactionHash)
	require.Equal(common.RequestStateFailed, tx.State)
}

func testAccountantSpentTransaction(_ context.Context, require *require.Assertions, raw string, testType int) {
	feeInputs := []*bitcoin.Input{{
		TransactionHash: "9b76c7a3f60063c59d11d9fdf11467fdf56d496c1dfa559c78d06da756d6e204",
		Index:           0,
		Satoshi:         50000,
	}}
	tx, err := bitcoin.SpendSignedTransaction(raw, feeInputs, testBitcoinKeyAccountantPriate, bitcoin.ChainBitcoin)
	require.Nil(err)
	rb, err := bitcoin.MarshalWiredTransaction(tx, wire.WitnessEncoding, bitcoin.ChainBitcoin)
	require.Nil(err)

	switch testType {
	case testHolderSigner:
		require.Equal("fcc2dc6e90d454ec76cc48925096281735ed85ccd93a73b87cd303be9f28478e", tx.TxHash().String())
	case testSignerObserver, testHolderObserver:
		require.Equal("09f837325c7285c2e118942536677926221a2eb882457b0f6aecc52b197aa201", tx.TxHash().String())
	}
	logger.Printf("%x", rb)
}

func testSafeApproveTransaction(ctx context.Context, require *require.Assertions, node *Node, transactionHash string, signers []*signer.Node) string {
	id := uuid.Must(uuid.NewV4()).String()

	tx, _ := node.store.ReadTransaction(ctx, transactionHash)
	require.Equal(common.RequestStateInitial, tx.State)
	safe, _ := node.store.ReadSafe(ctx, tx.Holder)

	hb := common.DecodeHexOrPanic(testBitcoinKeyHolderPrivate)
	holder, _ := btcec.PrivKeyFromBytes(hb)
	psTx, _ := bitcoin.UnmarshalPartiallySignedTransaction(common.DecodeHexOrPanic(tx.RawTransaction))
	for idx := range psTx.UnsignedTx.TxIn {
		hash := psTx.SigHash(idx)
		sig := ecdsa.Sign(holder, hash).Serialize()
		psTx.Inputs[idx].PartialSigs = []*psbt.PartialSig{{
			PubKey:    holder.PubKey().SerializeCompressed(),
			Signature: sig,
		}}
	}
	raw := psTx.Marshal()
	ref := crypto.Sha256Hash(raw)
	err := node.store.WriteProperty(ctx, ref.String(), base64.RawURLEncoding.EncodeToString(raw))
	require.Nil(err)
	extra := uuid.Must(uuid.FromString(tx.RequestId)).Bytes()
	extra = append(extra, ref[:]...)

	out := testBuildObserverRequest(node, id, testPublicKey(testBitcoinKeyHolderPrivate), common.ActionBitcoinSafeApproveTransaction, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	requests, err := node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateInitial)
	require.Nil(err)
	require.Len(requests, 2)
	tx, _ = node.store.ReadTransaction(ctx, transactionHash)
	require.Equal(common.RequestStatePending, tx.State)

	msg, _ := hex.DecodeString(requests[0].Message)
	out = testBuildSignerOutput(node, requests[0].RequestId, safe.Signer, common.OperationTypeSignInput, msg, common.CurveSecp256k1ECDSABitcoin)
	op := signer.TestProcessOutput(ctx, require, signers, out, requests[0].RequestId)
	out = testBuildSignerOutput(node, requests[0].RequestId, safe.Signer, common.OperationTypeSignOutput, op.Extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStatePending)
	require.Len(requests, 1)
	requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateInitial)
	require.Len(requests, 1)
	tx, _ = node.store.ReadTransaction(ctx, transactionHash)
	require.Equal(common.RequestStatePending, tx.State)

	msg, _ = hex.DecodeString(requests[1].Message)
	out = testBuildSignerOutput(node, requests[1].RequestId, safe.Signer, common.OperationTypeSignInput, msg, common.CurveSecp256k1ECDSABitcoin)
	op = signer.TestProcessOutput(ctx, require, signers, out, requests[1].RequestId)
	out = testBuildSignerOutput(node, requests[1].RequestId, safe.Signer, common.OperationTypeSignOutput, op.Extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateInitial)
	require.Len(requests, 0)
	requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStatePending)
	require.Len(requests, 0)
	requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateDone)
	require.Len(requests, 2)
	tx, _ = node.store.ReadTransaction(ctx, transactionHash)
	require.Equal(common.RequestStateDone, tx.State)

	signed := make(map[int][]byte)
	for _, r := range requests {
		b, _ := hex.DecodeString(r.Signature.String)
		signed[r.InputIndex] = b
	}
	mb := common.DecodeHexOrPanic(tx.RawTransaction)
	sTraceId := crypto.Blake3Hash([]byte(common.Base91Encode(mb))).String()
	sTraceId = mtg.UniqueId(sTraceId, sTraceId)
	rid := common.UniqueId(transactionHash, sTraceId)
	b := testReadObserverResponse(ctx, require, node, rid, common.ActionBitcoinSafeApproveTransaction)
	require.Equal(mb, b)

	msgTx := node.testSignerHolderApproveTransaction(ctx, require, tx.RawTransaction, signed, safe.Signer, safe.Path)
	signedBuffer, _ := bitcoin.MarshalWiredTransaction(msgTx, wire.WitnessEncoding, bitcoin.ChainBitcoin)
	signedRaw := hex.EncodeToString(signedBuffer)
	logger.Println(signedRaw)
	return signedRaw
}

func testSafeProposeTransaction(ctx context.Context, require *require.Assertions, node *Node, bondId string, rid, rhash, rraw string) string {
	holder := testPublicKey(testBitcoinKeyHolderPrivate)
	info, _ := node.store.ReadLatestNetworkInfo(ctx, common.SafeChainBitcoin, time.Now())
	extra := []byte{0}
	extra = append(extra, uuid.Must(uuid.FromString(info.RequestId)).Bytes()...)
	extra = append(extra, []byte(testTransactionReceiver)...)
	out := testBuildHolderRequest(node, rid, holder, common.ActionBitcoinSafeProposeTransaction, bondId, extra, decimal.NewFromFloat(0.000123))
	testStep(ctx, require, node, out)

	b := testReadObserverResponse(ctx, require, node, rid, common.ActionBitcoinSafeProposeTransaction)
	require.Equal(rraw, hex.EncodeToString(b))
	psbt, err := bitcoin.UnmarshalPartiallySignedTransaction(b)
	require.Nil(err)
	require.Equal(rhash, psbt.Hash())

	tx := psbt.UnsignedTx
	require.Len(tx.TxOut, 3)
	main := tx.TxOut[0]
	require.Equal(int64(12300), main.Value)
	script, _ := txscript.ParsePkScript(main.PkScript)
	addr, _ := script.Address(&chaincfg.MainNetParams)
	require.Equal(testTransactionReceiver, addr.EncodeAddress())
	change := tx.TxOut[1]
	require.Equal(int64(174260), change.Value)
	script, _ = txscript.ParsePkScript(change.PkScript)
	addr, _ = script.Address(&chaincfg.MainNetParams)
	require.Equal(testSafeAddress, addr.EncodeAddress())

	stx, err := node.store.ReadTransaction(ctx, psbt.Hash())
	require.Nil(err)
	require.Equal(hex.EncodeToString(psbt.Marshal()), stx.RawTransaction)
	require.Equal("[{\"amount\":\"0.000123\",\"receiver\":\"bc1ql0up0wwazxt6xlj84u9fnvhnagjjetcn7h4z5xxvd0kf5xuczjgqq2aehc\"}]", stx.Data)
	require.Equal(common.RequestStateInitial, stx.State)

	return stx.TransactionHash
}

func testSafeProposeRecoveryTransaction(ctx context.Context, require *require.Assertions, node *Node, bondId string, rid, rhash, rraw string) string {
	holder := testPublicKey(testBitcoinKeyHolderPrivate)
	info, _ := node.store.ReadLatestNetworkInfo(ctx, common.SafeChainBitcoin, time.Now())
	extra := []byte{1}
	extra = append(extra, uuid.Must(uuid.FromString(info.RequestId)).Bytes()...)
	extra = append(extra, []byte(testTransactionReceiver)...)
	out := testBuildHolderRequest(node, rid, holder, common.ActionBitcoinSafeProposeTransaction, bondId, extra, decimal.NewFromFloat(0.001))
	testStep(ctx, require, node, out)

	pendings, err := node.store.ListPendingBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(pendings, 1)

	b := testReadObserverResponse(ctx, require, node, rid, common.ActionBitcoinSafeProposeTransaction)
	require.Equal(rraw, hex.EncodeToString(b))
	psbt, err := bitcoin.UnmarshalPartiallySignedTransaction(b)
	require.Nil(err)
	require.Equal(rhash, psbt.Hash())

	tx := psbt.UnsignedTx
	require.Len(tx.TxOut, 2)
	main := tx.TxOut[0]
	require.Equal(int64(100000), main.Value)
	script, _ := txscript.ParsePkScript(main.PkScript)
	addr, _ := script.Address(&chaincfg.MainNetParams)
	require.Equal(testTransactionReceiver, addr.EncodeAddress())

	stx, err := node.store.ReadTransaction(ctx, psbt.Hash())
	require.Nil(err)
	require.Equal(hex.EncodeToString(psbt.Marshal()), stx.RawTransaction)
	require.Equal("[{\"amount\":\"0.001\",\"receiver\":\"bc1ql0up0wwazxt6xlj84u9fnvhnagjjetcn7h4z5xxvd0kf5xuczjgqq2aehc\"}]", stx.Data)
	require.Equal(common.RequestStateInitial, stx.State)

	return stx.TransactionHash
}

func testHolderApproveTransaction(rawTransaction string) string {
	hb := common.DecodeHexOrPanic(testBitcoinKeyHolderPrivate)
	holder, _ := btcec.PrivKeyFromBytes(hb)

	psTx, _ := bitcoin.UnmarshalPartiallySignedTransaction(common.DecodeHexOrPanic(rawTransaction))
	for idx := range psTx.UnsignedTx.TxIn {
		hash := psTx.SigHash(idx)
		sig := ecdsa.Sign(holder, hash).Serialize()
		psTx.Inputs[idx].PartialSigs = []*psbt.PartialSig{{
			PubKey:    holder.PubKey().SerializeCompressed(),
			Signature: sig,
		}}
	}
	raw := psTx.Marshal()
	return hex.EncodeToString(raw)
}

func (node *Node) testSignerHolderApproveTransaction(ctx context.Context, require *require.Assertions, rawTransaction string, signed map[int][]byte, signer, path string) *wire.MsgTx {
	hb := common.DecodeHexOrPanic(testBitcoinKeyHolderPrivate)
	holder, _ := btcec.PrivKeyFromBytes(hb)

	b, _ := hex.DecodeString(rawTransaction)
	psbt, _ := bitcoin.UnmarshalPartiallySignedTransaction(b)
	msgTx := psbt.UnsignedTx
	for idx := range msgTx.TxIn {
		pop := msgTx.TxIn[idx].PreviousOutPoint
		hash := psbt.SigHash(idx)
		utxo, _, _ := node.store.ReadBitcoinUTXO(ctx, pop.Hash.String(), int(pop.Index))
		msig := signed[idx]
		if msig == nil {
			continue
		}

		msig = append(msig, byte(bitcoin.SigHashType))
		der, _ := ecdsa.ParseDERSignature(msig[:len(msig)-1])
		pub, _ := node.deriveBIP32WithPath(ctx, signer, common.DecodeHexOrPanic(path))
		signer, _ := btcutil.NewAddressPubKey(common.DecodeHexOrPanic(pub), &chaincfg.MainNetParams)
		require.True(der.Verify(hash, signer.PubKey()))

		msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, []byte{})
		msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, msig)

		signature := ecdsa.Sign(holder, hash)
		sig := append(signature.Serialize(), byte(bitcoin.SigHashType))
		msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, sig)
		msgTx.TxIn[idx].Witness = append(msgTx.TxIn[idx].Witness, utxo.Script)
	}

	return msgTx
}

func testSafeCloseAccount(ctx context.Context, require *require.Assertions, node *Node, holder, transactionHash, holderSignedRaw string, signers []*signer.Node, action *mtg.Action) string {
	for i := 0; i < 10; i++ {
		testUpdateNetworkStatus(ctx, require, node, 797082, "00000000000000000004f8a108a06a9f61389c7340d8a3fa431a534ff339402a")
	}

	safe, _ := node.store.ReadSafe(ctx, holder)
	observer := testGetDerivedObserverPrivate(require)

	if transactionHash != "" {
		id := uuid.Must(uuid.NewV4()).String()
		tx, _ := node.store.ReadTransaction(ctx, transactionHash)
		require.Equal(common.RequestStateInitial, tx.State)

		psTx, _ := bitcoin.UnmarshalPartiallySignedTransaction(common.DecodeHexOrPanic(tx.RawTransaction))
		for idx := range psTx.UnsignedTx.TxIn {
			hash := psTx.SigHash(idx)
			sig := ecdsa.Sign(observer, hash).Serialize()
			psTx.Inputs[idx].PartialSigs = []*psbt.PartialSig{{
				PubKey:    observer.PubKey().SerializeCompressed(),
				Signature: sig,
			}}
		}
		raw := psTx.Marshal()
		ref := crypto.Sha256Hash(raw)
		err := node.store.WriteProperty(ctx, ref.String(), base64.RawURLEncoding.EncodeToString(raw))
		require.Nil(err)
		extra := uuid.Must(uuid.FromString(tx.RequestId)).Bytes()
		extra = append(extra, ref[:]...)

		out := testBuildObserverRequest(node, id, testPublicKey(testBitcoinKeyHolderPrivate), common.ActionBitcoinSafeCloseAccount, extra, common.CurveSecp256k1ECDSABitcoin)
		testStep(ctx, require, node, out)
		requests, err := node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateInitial)
		require.Nil(err)
		require.Len(requests, 1)
		tx, _ = node.store.ReadTransaction(ctx, transactionHash)
		require.Equal(common.RequestStatePending, tx.State)

		msg, _ := hex.DecodeString(requests[0].Message)
		out = testBuildSignerOutput(node, requests[0].RequestId, safe.Signer, common.OperationTypeSignInput, msg, common.CurveSecp256k1ECDSABitcoin)
		op := signer.TestProcessOutput(ctx, require, signers, out, requests[0].RequestId)
		out = testBuildSignerOutput(node, requests[0].RequestId, safe.Signer, common.OperationTypeSignOutput, op.Extra, common.CurveSecp256k1ECDSABitcoin)
		testStep(ctx, require, node, out)
		requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStatePending)
		require.Len(requests, 0)
		requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateInitial)
		require.Len(requests, 0)
		requests, _ = node.store.ListAllSignaturesForTransaction(ctx, transactionHash, common.RequestStateDone)
		require.Len(requests, 1)
		tx, _ = node.store.ReadTransaction(ctx, transactionHash)
		require.Equal(common.RequestStateDone, tx.State)

		signed := make(map[int][]byte)
		for _, r := range requests {
			b, _ := hex.DecodeString(r.Signature.String)
			signed[r.InputIndex] = b
		}
		mb := common.DecodeHexOrPanic(tx.RawTransaction)

		sTraceId := crypto.Blake3Hash([]byte(common.Base91Encode(mb))).String()
		sTraceId = mtg.UniqueId(sTraceId, sTraceId)
		rid := common.UniqueId(transactionHash, sTraceId)
		b := testReadObserverResponse(ctx, require, node, rid, common.ActionBitcoinSafeApproveTransaction)
		require.Equal(mb, b)

		msgTx := node.testSignerHolderApproveTransaction(ctx, require, tx.RawTransaction, signed, safe.Signer, safe.Path)
		signedBuffer, _ := bitcoin.MarshalWiredTransaction(msgTx, wire.WitnessEncoding, bitcoin.ChainBitcoin)
		return hex.EncodeToString(signedBuffer)
	}

	pendings, err := node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	logger.Println("1 ListPendingBitcoinUTXOsForHolder:", len(pendings))

	psTx, _ := bitcoin.UnmarshalPartiallySignedTransaction(common.DecodeHexOrPanic(holderSignedRaw))
	for idx := range psTx.UnsignedTx.TxIn {
		hash := psTx.SigHash(idx)
		sig := ecdsa.Sign(observer, hash).Serialize()

		osig := &psbt.PartialSig{
			PubKey:    observer.PubKey().SerializeCompressed(),
			Signature: sig,
		}
		psTx.Inputs[idx].PartialSigs = append(psTx.Inputs[idx].PartialSigs, osig)
	}
	msgTx := psTx.UnsignedTx
	transactionHash = msgTx.TxHash().String()
	raw := psTx.Marshal()

	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	logger.Println("2 ListPendingBitcoinUTXOsForHolder:", len(pendings))

	ref := crypto.Sha256Hash(raw)
	err = node.store.WriteProperty(ctx, ref.String(), base64.RawURLEncoding.EncodeToString(raw))
	require.Nil(err)
	extra := uuid.Nil.Bytes()
	extra = append(extra, ref[:]...)
	id := uuid.FromBytesOrNil(msgTx.TxOut[1].PkScript[2:]).String()
	out := testBuildObserverRequest(node, id, testPublicKey(testBitcoinKeyHolderPrivate), common.ActionBitcoinSafeCloseAccount, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)

	stx := node.buildStorageTransaction(ctx, &common.Request{Sequence: action.Sequence, Output: action}, []byte(common.Base91Encode(raw)))
	require.NotNil(stx)
	rid := common.UniqueId(transactionHash, stx.TraceId)
	b := testReadObserverResponse(ctx, require, node, rid, common.ActionBitcoinSafeApproveTransaction)
	require.Equal(b, raw)

	pendings, err = node.store.ListPendingBitcoinUTXOsForHolder(ctx, safe.Holder)
	require.Nil(err)
	logger.Println("3 ListPendingBitcoinUTXOsForHolder:", len(pendings))

	tx, _ := node.store.ReadTransaction(ctx, transactionHash)
	logger.Println(tx)

	b, _ = hex.DecodeString(tx.RawTransaction)
	psbt, _ := bitcoin.UnmarshalPartiallySignedTransaction(b)
	msgTx = psbt.UnsignedTx
	signedBuffer, _ := bitcoin.MarshalWiredTransaction(msgTx, wire.WitnessEncoding, bitcoin.ChainBitcoin)

	return hex.EncodeToString(signedBuffer)
}

func testObserverHolderDeposit(ctx context.Context, require *require.Assertions, node *Node, signer, observer string, input *bitcoin.Input, t int) {
	id := uuid.Must(uuid.NewV4()).String()
	hash, _ := crypto.HashFromString(input.TransactionHash)
	extra := []byte{common.SafeChainBitcoin}
	extra = append(extra, uuid.Must(uuid.FromString(common.SafeBitcoinChainId)).Bytes()...)
	extra = append(extra, hash[:]...)
	extra = binary.BigEndian.AppendUint64(extra, uint64(input.Index))
	extra = append(extra, big.NewInt(input.Satoshi).Bytes()...)

	holder := testPublicKey(testBitcoinKeyHolderPrivate)
	wsa, _ := node.buildBitcoinWitnessAccountWithDerivation(ctx, holder, signer, observer, bitcoinDefaultDerivationPath(), testTimelockDuration, common.SafeChainBitcoin)

	out := testBuildObserverRequest(node, id, holder, common.ActionObserverHolderDeposit, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)

	mainInputs, err := node.store.ListAllBitcoinUTXOsForHolder(ctx, holder)
	require.Nil(err)
	require.Len(mainInputs, t)
	utxo := mainInputs[t-1]
	require.Equal(uint32(input.Index), utxo.Index)
	require.Equal(input.Satoshi, utxo.Satoshi)
	require.Equal(wsa.Script, utxo.Script)
	require.Equal(hash.String(), utxo.TransactionHash)
	require.Equal(uint32(6), wsa.Sequence)
	require.True(bitcoin.CheckMultisigHolderSignerScript(utxo.Script))
}

func testSafeProposeAccount(ctx context.Context, require *require.Assertions, node *Node, signer, observer string) (string, string) {
	id := uuid.Must(uuid.NewV4()).String()
	holder := testPublicKey(testBitcoinKeyHolderPrivate)
	extra := testRecipient()
	price := decimal.NewFromFloat(testAccountPriceAmount)
	out := testBuildHolderRequest(node, id, holder, common.ActionBitcoinSafeProposeAccount, testAccountPriceAssetId, extra, price)
	testStep(ctx, require, node, out)
	b := testReadObserverResponse(ctx, require, node, id, common.ActionBitcoinSafeProposeAccount)
	wsa, err := bitcoin.UnmarshalWitnessScriptAccount(b)
	require.Nil(err)
	require.Equal(testSafeAddress, wsa.Address)
	require.Equal(uint32(6), wsa.Sequence)

	safe, err := node.store.ReadSafeProposal(ctx, id)
	require.Nil(err)
	require.Equal(id, safe.RequestId)
	require.Equal(holder, safe.Holder)
	require.Equal(signer, safe.Signer)
	require.Equal(observer, safe.Observer)
	public, err := node.buildBitcoinWitnessAccountWithDerivation(ctx, holder, signer, observer, bitcoinDefaultDerivationPath(), testTimelockDuration, common.SafeChainBitcoin)
	require.Nil(err)
	require.Equal(testSafeAddress, public.Address)
	require.Equal(public.Address, safe.Address)
	require.Equal(byte(1), safe.Threshold)
	require.Len(safe.Receivers, 1)
	require.Equal(testSafeBondReceiverId, safe.Receivers[0])

	return id, wsa.Address
}

func testSafeApproveAccount(ctx context.Context, require *require.Assertions, node *Node, signer, observer string, rid, publicKey string) {
	id := uuid.Must(uuid.NewV4()).String()
	holder := testPublicKey(testBitcoinKeyHolderPrivate)
	ms := fmt.Sprintf("APPROVE:%s:%s", rid, publicKey)
	hash := bitcoin.HashMessageForSignature(ms, common.SafeChainBitcoin)
	hb, _ := hex.DecodeString(testBitcoinKeyHolderPrivate)
	hp, _ := btcec.PrivKeyFromBytes(hb)
	signature := ecdsa.Sign(hp, hash)
	extra := uuid.FromStringOrNil(rid).Bytes()
	extra = append(extra, signature.Serialize()...)
	out := testBuildObserverRequest(node, id, holder, common.ActionBitcoinSafeApproveAccount, extra, common.CurveSecp256k1ECDSABitcoin)
	testStep(ctx, require, node, out)
	b := testReadObserverResponse(ctx, require, node, id, common.ActionBitcoinSafeApproveAccount)
	wsa, err := bitcoin.UnmarshalWitnessScriptAccount(b)
	require.Nil(err)
	require.Equal(testSafeAddress, wsa.Address)
	require.Equal(uint32(6), wsa.Sequence)

	safe, err := node.store.ReadSafe(ctx, holder)
	require.Nil(err)
	require.Equal(id, safe.RequestId)
	require.Equal(holder, safe.Holder)
	require.Equal(signer, safe.Signer)
	require.Equal(observer, safe.Observer)
	public, err := node.buildBitcoinWitnessAccountWithDerivation(ctx, holder, signer, observer, bitcoinDefaultDerivationPath(), testTimelockDuration, common.SafeChainBitcoin)
	require.Nil(err)
	require.Equal(testSafeAddress, public.Address)
	require.Equal(public.Address, safe.Address)
	require.Equal(byte(1), safe.Threshold)
	require.Len(safe.Receivers, 1)
	require.Equal(testSafeBondReceiverId, safe.Receivers[0])
}

func testStep(ctx context.Context, require *require.Assertions, node *Node, out *mtg.Action) {
	txs1, asset := node.ProcessOutput(ctx, out)
	require.Equal("", asset)
	timestamp, err := node.timestamp(ctx)
	require.Nil(err)
	require.Equal(out.Sequence, timestamp)
	req, err := node.store.ReadPendingRequest(ctx)
	require.Nil(err)
	require.Nil(req)
	req, err = node.store.ReadLatestRequest(ctx)
	require.Nil(err)
	ar, handled, err := node.store.ReadActionResult(ctx, out.OutputId, req.Id)
	require.Nil(err)
	require.NotNil(ar)
	require.True(handled)
	require.Equal("", ar.Compaction)
	txs3, asset := node.ProcessOutput(ctx, out)
	require.Equal("", asset)
	for i, tx1 := range txs1 {
		tx2 := ar.Transactions[i]
		tx3 := txs3[i]
		tx1.AppId = out.AppId
		tx2.AppId = out.AppId
		tx3.AppId = out.AppId
		tx1.Sequence = out.Sequence
		tx2.Sequence = out.Sequence
		tx3.Sequence = out.Sequence
		id := common.UniqueId(tx1.OpponentAppId, "test")
		tx1.OpponentAppId = id
		tx2.OpponentAppId = id
		tx3.OpponentAppId = id
		require.True(tx1.Equal(tx2))
		require.True(tx2.Equal(tx3))
	}
}

func testSpareKeys(ctx context.Context, require *require.Assertions, node *Node, hc, sc, oc int, crv byte) {
	for r, c := range map[int]int{
		common.RequestRoleHolder:   hc,
		common.RequestRoleSigner:   sc,
		common.RequestRoleObserver: oc,
	} {
		skc, err := node.store.CountSpareKeys(ctx, crv, common.RequestFlagNone, r)
		require.Nil(err)
		require.Equal(c, skc)
	}
}

func testReadObserverResponse(ctx context.Context, require *require.Assertions, node *Node, id string, typ byte) []byte {
	v, _ := node.store.ReadProperty(ctx, id)
	var om map[string]any
	err := json.Unmarshal([]byte(v), &om)
	require.Nil(err)
	require.Equal(node.conf.ObserverUserId, om["receivers"].([]any)[0])
	switch typ {
	case common.ActionBitcoinSafeApproveAccount:
		params, _ := node.store.ReadLatestOperationParams(ctx, common.SafeChainBitcoin, time.Now())
		require.Equal(params.OperationPriceAsset, om["asset_id"])
		require.Equal(params.OperationPriceAmount.String(), om["amount"])
	case common.ActionEthereumSafeApproveAccount:
		params, _ := node.store.ReadLatestOperationParams(ctx, common.SafeChainPolygon, time.Now())
		require.Equal(params.OperationPriceAsset, om["asset_id"])
		require.Equal(params.OperationPriceAmount.String(), om["amount"])
	default:
		require.Equal(node.conf.ObserverAssetId, om["asset_id"])
		require.Equal("1", om["amount"])
	}
	b, _ := hex.DecodeString(om["memo"].(string))
	b = common.AESDecrypt(node.observerAESKey[:], b)
	op, err := common.DecodeOperation(b)
	require.Nil(err)
	require.Equal(typ, op.Type)
	require.Equal(id, op.Id)
	require.Len(op.Extra, 16)
	v, _ = node.store.ReadProperty(ctx, uuid.FromBytesOrNil(op.Extra).String())
	b, _ = hex.DecodeString(v)
	b, _ = common.Base91Decode(string(b))
	return b
}

func testBuildHolderRequest(node *Node, id, public string, action byte, assetId string, extra []byte, amount decimal.Decimal) *mtg.Action {
	crv := byte(common.CurveSecp256k1ECDSABitcoin)
	switch action {
	case common.ActionBitcoinSafeProposeAccount, common.ActionBitcoinSafeProposeTransaction:
	case common.ActionEthereumSafeProposeAccount, common.ActionEthereumSafeProposeTransaction:
		crv = common.CurveSecp256k1ECDSAPolygon
	}
	op := &common.Operation{
		Id:     id,
		Type:   action,
		Curve:  crv,
		Public: public,
		Extra:  extra,
	}
	memo := mtg.EncodeMixinExtraBase64(node.conf.AppId, op.Encode())
	memo = hex.EncodeToString([]byte(memo))
	return &mtg.Action{
		UnifiedOutput: mtg.UnifiedOutput{
			TransactionHash:    crypto.Sha256Hash([]byte(op.Id)).String(),
			OutputId:           common.UniqueId(op.Id, "output"),
			AppId:              node.conf.AppId,
			AssetId:            assetId,
			Extra:              memo,
			Amount:             amount,
			SequencerCreatedAt: time.Now(),
			Sequence:           sequence,
		},
	}
}

func testBuildObserverRequest(node *Node, id, public string, action byte, extra []byte, crv byte) *mtg.Action {
	sequence += 10

	op := &common.Operation{
		Id:     id,
		Type:   action,
		Curve:  crv,
		Public: public,
		Extra:  extra,
	}
	memo := mtg.EncodeMixinExtraBase64(node.conf.AppId, node.encryptObserverOperation(op))
	memo = hex.EncodeToString([]byte(memo))
	timestamp := time.Now()
	if action == common.ActionObserverAddKey {
		timestamp = timestamp.Add(-SafeKeyBackupMaturity)
	}
	return &mtg.Action{
		UnifiedOutput: mtg.UnifiedOutput{
			OutputId:           common.UniqueId(op.Id, "output"),
			TransactionHash:    crypto.Sha256Hash([]byte(op.Id)).String(),
			AppId:              node.conf.AppId,
			Senders:            []string{node.conf.ObserverUserId},
			AssetId:            node.conf.ObserverAssetId,
			Extra:              memo,
			Amount:             decimal.New(1, 1),
			SequencerCreatedAt: timestamp,
			Sequence:           sequence,
		},
	}
}

func testBuildSignerOutput(node *Node, id, public string, action byte, extra []byte, crv byte) *mtg.Action {
	sequence += 10

	path := bitcoinDefaultDerivationPath()
	switch crv {
	case common.CurveSecp256k1ECDSABitcoin:
	case common.CurveSecp256k1ECDSAEthereum, common.CurveSecp256k1ECDSAPolygon:
		path = ethereumDefaultDerivationPath()
	default:
		panic(crv)
	}
	op := &common.Operation{
		Id:    id,
		Type:  action,
		Curve: crv,
		Extra: extra,
	}
	timestamp := time.Now()
	appId := node.conf.AppId
	switch action {
	case common.OperationTypeKeygenInput:
		appId = node.conf.SignerAppId
		op.Public = hex.EncodeToString(common.Fingerprint(public))
	case common.OperationTypeSignInput:
		appId = node.conf.SignerAppId
		fingerPath := append(common.Fingerprint(public), path...)
		op.Public = hex.EncodeToString(fingerPath)
	case common.OperationTypeKeygenOutput:
		op.Public = public
		timestamp = timestamp.Add(-SafeKeyBackupMaturity)
	case common.OperationTypeSignOutput:
		op.Public = public
	}
	memo := mtg.EncodeMixinExtraBase64(appId, node.encryptSignerOperation(op))
	memo = hex.EncodeToString([]byte(memo))
	return &mtg.Action{
		UnifiedOutput: mtg.UnifiedOutput{
			OutputId:           id,
			TransactionHash:    crypto.Sha256Hash([]byte(op.Id)).String(),
			AppId:              appId,
			AssetId:            node.conf.AssetId,
			Extra:              memo,
			Amount:             decimal.New(1, 1),
			SequencerCreatedAt: timestamp,
			Sequence:           sequence,
		},
	}
}

func testDeployBondContract(ctx context.Context, require *require.Assertions, node *Node, addr, assetId string) string {
	safe, _ := node.store.ReadSafeByAddress(ctx, addr)
	asset, _ := node.fetchAssetMeta(ctx, assetId)
	err := abi.GetOrDeployFactoryAsset(ctx, node.conf.PolygonRPC, os.Getenv("MVM_DEPLOYER"), asset.AssetId, asset.Symbol, asset.Name, node.conf.PolygonKeeperDepositEntry, safe.Holder)
	require.Nil(err)
	bond := abi.GetFactoryAssetAddress(node.conf.PolygonKeeperDepositEntry, assetId, asset.Symbol, asset.Name, safe.Holder)
	assetKey := strings.ToLower(bond.String())
	err = ethereum.VerifyAssetKey(assetKey)
	require.Nil(err)
	asset, _ = node.fetchAssetMeta(ctx, ethereum.GenerateAssetId(common.SafeChainPolygon, assetKey))
	return asset.AssetId
}

func testBuildNode(ctx context.Context, require *require.Assertions, root string) (*Node, *mtg.SQLite3Store) {
	f, _ := os.ReadFile("../config/example.toml")
	var conf struct {
		Keeper *Configuration `toml:"keeper"`
		Signer struct {
			MTG *mtg.Configuration `toml:"mtg"`
		} `toml:"signer"`
	}
	err := toml.Unmarshal(f, &conf)
	require.Nil(err)
	conf.Keeper.MTG.GroupSize = 1

	if rpc := os.Getenv("POLYGONRPC"); rpc != "" {
		conf.Keeper.PolygonRPC = rpc
	}

	conf.Keeper.StoreDir = root
	if !(strings.HasPrefix(conf.Keeper.StoreDir, "/tmp/") || strings.HasPrefix(conf.Keeper.StoreDir, "/var/folders")) {
		panic(root)
	}
	err = os.MkdirAll(conf.Keeper.StoreDir, os.ModePerm)
	require.Nil(err)
	kd, err := OpenSQLite3Store(conf.Keeper.StoreDir + "/safe.sqlite3")
	require.Nil(err)

	db, err := mtg.OpenSQLite3Store(conf.Keeper.StoreDir + "/mtg.sqlite3")
	require.Nil(err)
	group, err := mtg.BuildGroup(ctx, db, conf.Keeper.MTG)
	require.Nil(err)
	require.NotNil(group)
	// FIXME this actually has no effect because we are not using group.Run()
	group.EnableDebug()

	for i := range 1000 {
		sequence += uint64(i)
		_, err = testWriteOutput(ctx, db, conf.Keeper.AppId, conf.Keeper.AssetId, "", uint64(sequence), decimal.NewFromInt(1))
		require.Nil(err)
	}
	sequence += uint64(1)
	for i := range 1000 {
		sequence += uint64(i)
		_, err = testWriteOutput(ctx, db, conf.Keeper.AppId, conf.Keeper.ObserverAssetId, "", uint64(sequence), decimal.NewFromInt(1))
		require.Nil(err)
	}
	sequence += uint64(1)
	for i := range 1000 {
		sequence += uint64(i)
		_, err = testWriteOutput(ctx, db, conf.Keeper.AppId, testAccountPriceAssetId, "", uint64(sequence), decimal.NewFromInt(1))
		require.Nil(err)
	}
	sequence += uint64(1)
	for i := range 1000 {
		sequence += uint64(i)
		_, err = testWriteOutput(ctx, db, conf.Keeper.AppId, mtg.StorageAssetId, "", uint64(sequence), decimal.NewFromInt(1))
		require.Nil(err)
	}
	sequence += uint64(100)

	var client *mixin.Client
	node := NewNode(kd, group, conf.Keeper, conf.Signer.MTG, client)
	group.AttachWorker(node.conf.AppId, node)
	return node, db
}

func testWriteOutput(ctx context.Context, db *mtg.SQLite3Store, appId, assetId, extra string, sequence uint64, amount decimal.Decimal) (*mtg.UnifiedOutput, error) {
	id := uuid.Must(uuid.NewV4())
	output := &mtg.UnifiedOutput{
		OutputId:           id.String(),
		AppId:              appId,
		AssetId:            assetId,
		Amount:             amount,
		Sequence:           sequence,
		SequencerCreatedAt: time.Now(),
		TransactionHash:    crypto.Sha256Hash(id.Bytes()).String(),
		State:              mtg.SafeUtxoStateUnspent,
		Extra:              extra,
	}
	err := db.WriteAction(ctx, output, mtg.ActionStateInitial)
	return output, err
}

func testRecipient() []byte {
	extra := binary.BigEndian.AppendUint16(nil, uint16(testTimelockDuration/time.Hour))
	extra = append(extra, 1, 1)
	id := uuid.FromStringOrNil(testSafeBondReceiverId)
	return append(extra, id.Bytes()...)
}

func testPublicKey(priv string) string {
	seed, _ := hex.DecodeString(priv)
	_, dk := btcec.PrivKeyFromBytes(seed)
	return hex.EncodeToString(dk.SerializeCompressed())
}

func testGetDerivedObserverPrivate(require *require.Assertions) *btcec.PrivateKey {
	path8 := []byte{2, 0, 0, 0}
	children := make([]uint32, path8[0])
	for i := 0; i < int(path8[0]); i++ {
		children[i] = uint32(path8[1+i])
	}

	key, err := hex.DecodeString(testBitcoinKeyObserverPrivate)
	require.Nil(err)
	chainCode, err := hex.DecodeString(testBitcoinKeyObserverChainCode)
	require.Nil(err)

	parentFP := []byte{0x00, 0x00, 0x00, 0x00}
	version := []byte{0x04, 0x88, 0xb2, 0x1e}
	extPub := hdkeychain.NewExtendedKey(version, key, chainCode, parentFP, 0, 0, true)
	for _, i := range children {
		extPub, err = extPub.Derive(i)
		require.Nil(err)
		if bytes.Equal(extPub.ChainCode(), chainCode) {
			panic(i)
		}
	}

	priv, err := extPub.ECPrivKey()
	require.Nil(err)
	return priv
}

func testGenerateDummyExtra(node *Node) string {
	e := mtg.EncodeMixinExtraBase64(node.conf.AppId, nil)
	return hex.EncodeToString([]byte(e))
}
