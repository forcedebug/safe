package ethereum

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	mc "github.com/MixinNetwork/mixin/common"
	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/safe/apps/bitcoin"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// create a gnosis safe contract with 2/3 multisig
// with safe guard to do time lock of observer
// with deploy2 to determine exact contract address

type GnosisSafe struct {
	Sequence uint32
	Address  string
	TxHash   string
}

func (gs *GnosisSafe) Marshal() []byte {
	enc := mc.NewEncoder()
	enc.WriteUint32(gs.Sequence)
	bitcoin.WriteBytes(enc, []byte(gs.Address))
	bitcoin.WriteBytes(enc, []byte(gs.TxHash))
	return enc.Bytes()
}

func UnmarshalGnosisSafe(extra []byte) (*GnosisSafe, error) {
	dec := mc.NewDecoder(extra)
	sequence, err := dec.ReadUint32()
	if err != nil {
		return nil, err
	}
	addr, err := dec.ReadBytes()
	if err != nil {
		return nil, err
	}
	hash, err := dec.ReadBytes()
	if err != nil {
		return nil, err
	}
	return &GnosisSafe{
		Sequence: sequence,
		Address:  string(addr),
		TxHash:   string(hash),
	}, nil
}

func BuildGnosisSafe(ctx context.Context, rpc, holder, signer, observer, rid string, lock time.Duration, chain byte) (*GnosisSafe, *SafeTransaction, error) {
	owners, _ := GetSortedSafeOwners(holder, signer, observer)
	safeAddress := GetSafeAccountAddress(owners, 2).Hex()
	ob, err := ParseEthereumCompressedPublicKey(observer)
	if err != nil {
		return nil, nil, fmt.Errorf("ethereum.ParseEthereumCompressedPublicKey(%s) => %v %v", observer, ob, err)
	}

	if lock < TimeLockMinimum || lock > TimeLockMaximum {
		return nil, nil, fmt.Errorf("time lock out of range %s", lock.String())
	}
	sequence := lock / time.Hour

	chainID := GetEvmChainID(int64(chain))
	t, err := CreateEnableGuardTransaction(ctx, chainID, rid, safeAddress, ob.Hex(), new(big.Int).SetUint64(uint64(sequence)))
	logger.Printf("CreateEnableGuardTransaction(%d, %s, %s, %s, %d) => %v", chainID, rid, safeAddress, ob.Hex(), sequence, err)
	if err != nil {
		return nil, nil, err
	}

	return &GnosisSafe{
		Sequence: uint32(sequence),
		Address:  safeAddress,
		TxHash:   t.TxHash,
	}, t, nil
}

func GetSortedSafeOwners(holder, signer, observer string) ([]string, []string) {
	var owners []string
	for _, pub := range []string{holder, signer, observer} {
		addr, err := ParseEthereumCompressedPublicKey(pub)
		if err != nil {
			panic(pub)
		}
		owners = append(owners, addr.Hex())
	}
	sort.Slice(owners, func(i, j int) bool { return common.HexToAddress(owners[i]).Cmp(common.HexToAddress(owners[j])) == -1 })
	addressMap := make(map[string]int)
	for i, a := range owners {
		addressMap[a] = i
	}

	pubs := make([]string, 3)
	for _, pub := range []string{holder, signer, observer} {
		addr, _ := ParseEthereumCompressedPublicKey(pub)
		index := addressMap[addr.Hex()]
		pubs[index] = pub
	}
	return owners, pubs
}

func GetOrDeploySafeAccount(ctx context.Context, rpc, key string, chainId int64, owners []string, threshold int64, timelock, observerIndex int64, tx *SafeTransaction) (*common.Address, error) {
	addr := GetSafeAccountAddress(owners, threshold)

	isGuarded, isDeployed, err := CheckSafeAccountDeployed(rpc, addr.String())
	if err != nil {
		return nil, err
	}
	if !isDeployed {
		err = DeploySafeAccount(ctx, rpc, key, chainId, owners, threshold)
		if err != nil {
			return nil, err
		}
	}
	if !isGuarded {
		_, err := tx.ExecTransaction(ctx, rpc, key)
		if err != nil {
			return nil, err
		}
	}
	return &addr, nil
}

func GetSafeAccountGuard(rpc, address string) (string, error) {
	conn, abi, err := safeInit(rpc, address)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	bGuardOffet, err := hex.DecodeString(guardStorageSlot[2:])
	if err != nil {
		return "", err
	}
	bGuard, err := abi.GetStorageAt(nil, new(big.Int).SetBytes(bGuardOffet), new(big.Int).SetInt64(1))
	if err != nil {
		if strings.Contains(err.Error(), "no contract code at given address") {
			return "", nil
		}
		return "", err
	}
	guardAddress := common.BytesToAddress(bGuard)
	return guardAddress.Hex(), nil
}

func CheckSafeAccountDeployed(rpc, address string) (bool, bool, error) {
	guardAddress, err := GetSafeAccountGuard(rpc, address)
	if err != nil {
		return false, false, err
	}
	switch guardAddress {
	case "":
		return false, false, nil
	case EthereumEmptyAddress:
		return false, true, nil
	default:
		return true, true, nil
	}
}

func GetSafeAccountAddress(owners []string, threshold int64) common.Address {
	sort.Slice(owners, func(i, j int) bool { return common.HexToAddress(owners[i]).Cmp(common.HexToAddress(owners[j])) == -1 })

	this, err := hex.DecodeString(EthereumSafeProxyFactoryAddress[2:])
	if err != nil {
		panic(err)
	}

	initializer := getInitializer(owners, threshold)
	nonce := new(big.Int)
	nonce.SetString(predeterminedSaltNonce[2:], 16)
	encodedNonce := packSaltArguments(nonce)
	salt := crypto.Keccak256(initializer)
	salt = append(salt, encodedNonce...)
	salt = crypto.Keccak256(salt)

	code, err := hex.DecodeString(accountContractCode[2:])
	if err != nil {
		panic(err)
	}
	code = append(code, packSafeArguments(EthereumSafeL2Address)...)

	input := []byte{0xff}
	input = append(input, this...)
	input = append(input, math.U256Bytes(new(big.Int).SetBytes(salt))...)
	input = append(input, crypto.Keccak256(code)...)
	return common.BytesToAddress(crypto.Keccak256(input))
}

func DeploySafeAccount(ctx context.Context, rpc, key string, chainId int64, owners []string, threshold int64) error {
	initializer := getInitializer(owners, threshold)
	nonce := new(big.Int)
	nonce.SetString(predeterminedSaltNonce[2:], 16)

	conn, factoryAbi, err := factoryInit(rpc)
	if err != nil {
		return err
	}
	defer conn.Close()

	signer := SignerInit(ctx, conn, key, chainId)

	t, err := factoryAbi.CreateProxyWithNonce(signer, common.HexToAddress(EthereumSafeL2Address), initializer, nonce)
	if err != nil {
		return err
	}
	_, err = bind.WaitMined(ctx, conn, t)
	return err
}

func GetSafeLastTxTime(rpc, address string) (time.Time, error) {
	guardAddress, err := GetSafeAccountGuard(rpc, address)
	if err != nil {
		return time.Time{}, err
	}
	switch guardAddress {
	case "", EthereumEmptyAddress:
		panic(fmt.Errorf("safe %s is not deployed or guard is not enabled", address))
	}

	conn, abi, err := guardInit(rpc, guardAddress)
	if err != nil {
		return time.Time{}, err
	}
	defer conn.Close()

	addr := common.HexToAddress(address)
	timestamp, err := abi.SafeLastTxTime(nil, addr)
	if err != nil {
		return time.Time{}, err
	}
	t := time.Unix(timestamp.Int64(), 0)
	return t, nil
}

func VerifyHolderKey(public string) error {
	_, err := ParseEthereumCompressedPublicKey(public)
	return err
}

func VerifyMessageSignature(public string, msg, sig []byte) error {
	hash := HashMessageForSignature(hex.EncodeToString(msg))
	return VerifyHashSignature(public, hash, sig)
}

func VerifyHashSignature(public string, hash, sig []byte) error {
	pub, err := hex.DecodeString(public)
	if err != nil {
		panic(public)
	}
	signed := crypto.VerifySignature(pub, hash, sig[:64])
	if signed {
		return nil
	}
	return fmt.Errorf("crypto.VerifySignature(%s, %x, %x)", public, hash, sig)
}

func ParseEthereumCompressedPublicKey(public string) (*common.Address, error) {
	pub, err := hex.DecodeString(public)
	if err != nil {
		return nil, err
	}

	publicKey, err := crypto.DecompressPubkey(pub)
	if err != nil {
		return nil, err
	}

	addr := crypto.PubkeyToAddress(*publicKey)
	return &addr, nil
}

func FetchBalanceFromKey(ctx context.Context, rpc, key string) (*big.Int, error) {
	addr, err := PrivToAddress(key)
	if err != nil {
		return nil, err
	}
	client, err := ethclient.Dial(rpc)
	if err != nil {
		return nil, err
	}

	balance, err := client.BalanceAt(ctx, *addr, nil)
	if err != nil {
		return nil, err
	}
	return balance, nil
}

func getInitializer(owners []string, threshold int64) []byte {
	blankAddress := common.HexToAddress(EthereumEmptyAddress)
	handlerAddress := common.HexToAddress(EthereumCompatibilityFallbackHandlerAddress)
	initializer := packSetupArguments(
		owners, threshold, nil, blankAddress, handlerAddress, blankAddress, blankAddress, big.NewInt(0),
	)
	return initializer
}
