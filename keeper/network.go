package keeper

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/MixinNetwork/mixin/crypto"
	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/safe/apps/bitcoin"
	"github.com/MixinNetwork/safe/apps/ethereum"
	"github.com/MixinNetwork/safe/common"
	"github.com/MixinNetwork/safe/keeper/store"
	"github.com/MixinNetwork/trusted-group/mtg"
	"github.com/gofrs/uuid/v5"
	"github.com/shopspring/decimal"
)

const (
	bitcoinMinimumFeeRate = 1
	bitcoinMaximumFeeRate = 1000
)

func (node *Node) writeNetworkInfo(ctx context.Context, req *common.Request) ([]*mtg.Transaction, string) {
	logger.Printf("node.writeNetworkInfo(%v)", req)
	if req.Role != common.RequestRoleObserver {
		panic(req.Role)
	}
	extra := req.ExtraBytes()
	if len(extra) < 17 {
		return node.failRequest(ctx, req, "")
	}

	info := &store.NetworkInfo{
		RequestId: req.Id,
		CreatedAt: req.CreatedAt,
	}
	info.Chain = extra[0]
	info.Fee = binary.BigEndian.Uint64(extra[1:9])
	info.Height = binary.BigEndian.Uint64(extra[9:17])

	old, err := node.store.ReadLatestNetworkInfo(ctx, info.Chain, req.CreatedAt)
	logger.Printf("store.ReadLatestNetworkInfo(%s, %v) => %v", req.Id, info, old)
	if err != nil {
		panic(fmt.Errorf("store.ReadLatestNetworkInfo(%d) => %v", info.Chain, err))
	} else if old != nil && old.RequestId == req.Id {
		return node.failRequest(ctx, req, "")
	} else if old != nil && old.Height > info.Height {
		return node.failRequest(ctx, req, "")
	}

	if info.Chain != common.SafeCurveChain(req.Curve) {
		panic(req.Id)
	}
	switch info.Chain {
	case common.SafeChainBitcoin, common.SafeChainLitecoin:
		info.Hash = hex.EncodeToString(extra[17:])
		valid, err := node.verifyBitcoinNetworkInfo(info, old)
		logger.Printf("node.verifyBitcoinNetworkInfo(%s, %v) => %t", req.Id, info, valid)
		if err != nil {
			panic(fmt.Errorf("node.verifyBitcoinNetworkInfo(%v) => %v", info, err))
		} else if !valid {
			return node.failRequest(ctx, req, "")
		}
	case common.SafeChainEthereum, common.SafeChainPolygon:
		info.Hash = "0x" + hex.EncodeToString(extra[17:])
		valid, err := node.verifyEthereumNetworkInfo(info, old)
		logger.Printf("node.verifyEthereumNetworkInfo(%s, %v) => %t", req.Id, info, valid)
		if err != nil {
			panic(fmt.Errorf("node.verifyEthereumNetworkInfo(%v) => %v", info, err))
		} else if !valid {
			return node.failRequest(ctx, req, "")
		}
	default:
		return node.failRequest(ctx, req, "")
	}
	err = node.store.WriteNetworkInfoFromRequest(ctx, info, req)
	if err != nil {
		panic(err)
	}
	return nil, ""
}

func (node *Node) writeOperationParams(ctx context.Context, req *common.Request) ([]*mtg.Transaction, string) {
	logger.Printf("node.writeOperationParams(%v)", req)
	if req.Role != common.RequestRoleObserver {
		panic(req.Role)
	}
	extra := req.ExtraBytes()
	if len(extra) != 33 {
		return node.failRequest(ctx, req, "")
	}

	chain := extra[0]
	if chain != common.SafeCurveChain(req.Curve) {
		panic(req.Id)
	}
	switch chain {
	case common.SafeChainBitcoin:
	case common.SafeChainLitecoin:
	case common.SafeChainEthereum:
	case common.SafeChainPolygon:
	default:
		return node.failRequest(ctx, req, "")
	}

	assetId := uuid.Must(uuid.FromBytes(extra[1:17]))
	abu := new(big.Int).SetUint64(binary.BigEndian.Uint64(extra[17:25]))
	amount := decimal.NewFromBigInt(abu, -8)
	mbu := new(big.Int).SetUint64(binary.BigEndian.Uint64(extra[25:33]))
	minimum := decimal.NewFromBigInt(mbu, -8)
	params := &store.OperationParams{
		RequestId:            req.Id,
		Chain:                chain,
		OperationPriceAsset:  assetId.String(),
		OperationPriceAmount: amount,
		TransactionMinimum:   minimum,
		CreatedAt:            req.CreatedAt,
	}
	err := node.store.WriteOperationParamsFromRequest(ctx, params, req)
	if err != nil {
		panic(err)
	}
	return nil, ""
}

func (node *Node) verifyBitcoinNetworkInfo(info, old *store.NetworkInfo) (bool, error) {
	if len(info.Hash) != 64 {
		return false, nil
	}
	if old != nil && old.Hash == info.Hash {
		if old.Height != info.Height {
			return false, fmt.Errorf("malicious bitcoin block %s", info.Hash)
		}
	} else {
		rpc, _ := node.bitcoinParams(info.Chain)
		block, err := bitcoin.RPCGetBlock(rpc, info.Hash)
		if err != nil || block == nil {
			return false, fmt.Errorf("malicious bitcoin block or node not in sync? %s %v", info.Hash, err)
		}
		if block.Height != info.Height {
			return false, fmt.Errorf("malicious bitcoin block %s", info.Hash)
		}
		if block.Confirmations == -1 {
			return false, fmt.Errorf("malicious bitcoin fork %s", info.Hash)
		}
	}
	if info.Fee < bitcoinMinimumFeeRate || info.Fee > bitcoinMaximumFeeRate {
		return false, nil
	}
	return true, nil
}

func (node *Node) verifyEthereumNetworkInfo(info, old *store.NetworkInfo) (bool, error) {
	if len(info.Hash) != 66 {
		return false, nil
	}
	if old != nil && old.Hash == info.Hash {
		if old.Height != info.Height {
			return false, fmt.Errorf("malicious bitcoin block %s", info.Hash)
		}
	} else {
		rpc, _ := node.ethereumParams(info.Chain)
		block, err := ethereum.RPCGetBlock(rpc, info.Hash)
		if err != nil || block == nil {
			return false, fmt.Errorf("malicious ethereum block or node not in sync? %s %v", info.Hash, err)
		}
		if block.Height != info.Height {
			return false, fmt.Errorf("malicious ethereum block %s", info.Hash)
		}
	}
	return true, nil
}

func (node *Node) bitcoinParams(chain byte) (string, string) {
	switch chain {
	case common.SafeChainBitcoin:
		return node.conf.BitcoinRPC, common.SafeBitcoinChainId
	case common.SafeChainLitecoin:
		return node.conf.LitecoinRPC, common.SafeLitecoinChainId
	default:
		panic(chain)
	}
}

func (node *Node) ethereumParams(chain byte) (string, string) {
	switch chain {
	case common.SafeChainEthereum:
		return node.conf.EthereumRPC, common.SafeEthereumChainId
	case common.SafeChainPolygon:
		return node.conf.PolygonRPC, common.SafePolygonChainId
	default:
		panic(chain)
	}
}

func (node *Node) fetchAssetMetaFromMessengerOrEthereum(ctx context.Context, id, assetContract string, chain byte) (*store.Asset, error) {
	meta, err := node.fetchAssetMeta(ctx, id)
	if err != nil || meta != nil {
		return meta, err
	}
	switch chain {
	case common.SafeChainEthereum:
	case common.SafeChainPolygon:
	default:
		panic(chain)
	}
	rpc, _ := node.ethereumParams(chain)
	token, err := ethereum.FetchAsset(chain, rpc, assetContract)
	if err != nil {
		return nil, err
	}
	asset := &store.Asset{
		AssetId:   token.Id,
		MixinId:   crypto.Sha256Hash([]byte(token.Id)).String(),
		AssetKey:  token.Address,
		Symbol:    token.Symbol,
		Name:      token.Name,
		Decimals:  token.Decimals,
		Chain:     token.Chain,
		CreatedAt: time.Now().UTC(),
	}
	return asset, node.store.WriteAssetMeta(ctx, asset)
}

func (node *Node) fetchMixinAsset(_ context.Context, id string) (*store.Asset, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	path := node.conf.MixinMessengerAPI + "/network/assets/" + id
	resp, err := client.Get(path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var body struct {
		Data *struct {
			AssetId   string      `json:"asset_id"`
			MixinId   crypto.Hash `json:"mixin_id"`
			AssetKey  string      `json:"asset_key"`
			Symbol    string      `json:"symbol"`
			Name      string      `json:"name"`
			Precision uint32      `json:"precision"`
			ChainId   string      `json:"chain_id"`
		} `json:"data"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	if err != nil {
		return nil, err
	}
	asset := body.Data

	return &store.Asset{
		AssetId:   asset.AssetId,
		MixinId:   asset.MixinId.String(),
		AssetKey:  asset.AssetKey,
		Symbol:    asset.Symbol,
		Name:      asset.Name,
		Decimals:  asset.Precision,
		Chain:     common.SafeAssetIdChain(asset.ChainId),
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (node *Node) fetchAssetMeta(ctx context.Context, id string) (*store.Asset, error) {
	meta, err := node.store.ReadAssetMeta(ctx, id)
	if err != nil || meta != nil {
		return meta, err
	}

	for {
		meta, err = node.fetchMixinAsset(ctx, id)
		if err == nil {
			return meta, node.store.WriteAssetMeta(ctx, meta)
		}
		reason := strings.ToLower(err.Error())
		switch {
		case strings.Contains(reason, "timeout"):
		case strings.Contains(reason, "eof"):
		case strings.Contains(reason, "handshake"):
		default:
			return nil, err
		}
		time.Sleep(2 * time.Second)
	}
}
