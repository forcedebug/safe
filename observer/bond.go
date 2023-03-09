package observer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MixinNetwork/mixin/crypto"
	"github.com/MixinNetwork/mixin/domains/mvm"
	"github.com/MixinNetwork/mixin/logger"
	"github.com/MixinNetwork/safe/apps/bitcoin"
	"github.com/MixinNetwork/safe/common/abi"
	"github.com/MixinNetwork/safe/keeper"
)

func (node *Node) deployBitcoinSafeBond(ctx context.Context, data []byte) error {
	logger.Printf("node.deployBitcoinSafeBond(%x)", data)
	wsa, accountant, err := bitcoin.UnmarshalWitnessScriptAccountWitAccountant(data)
	if err != nil {
		return fmt.Errorf("bitcoin.UnmarshalWitnessScriptAccountWitAccountant(%x) => %v", data, err)
	}
	safe, err := node.keeperStore.ReadSafeByAddress(ctx, wsa.Address)
	if err != nil {
		return fmt.Errorf("keeperStore.ReadSafeByAddress(%s) => %v", wsa.Address, err)
	}
	holder, err := node.keeperStore.ReadAccountantHolder(ctx, accountant)
	if err != nil {
		return fmt.Errorf("keeperStore.ReadAccountantHolder(%s) => %v", accountant, err)
	}
	if safe.Holder != holder {
		return fmt.Errorf("malformed safe accountant %x", data)
	}
	_, err = node.checkOrDeployKeeperBond(ctx, keeper.SafeBitcoinChainId, holder)
	logger.Printf("node.checkOrDeployKeeperBond(%s, %s) => %v", keeper.SafeBitcoinChainId, holder, err)
	return err
}

func (node *Node) checkOrDeployKeeperBond(ctx context.Context, assetId, holder string) (bool, error) {
	rpc, key := node.conf.MVMRPC, node.conf.MVMKey
	asset, err := node.fetchAssetMeta(ctx, assetId)
	if err != nil {
		return false, fmt.Errorf("node.fetchAssetMeta(%s) => %v", assetId, err)
	}

	addr := abi.GetFactoryAssetAddress(assetId, asset.Symbol, asset.Name, holder)
	assetKey := strings.ToLower(addr.String())
	err = mvm.VerifyAssetKey(assetKey)
	if err != nil {
		return false, fmt.Errorf("mvm.VerifyAssetKey(%s) => %v", assetKey, err)
	}

	bondId := mvm.GenerateAssetId(assetKey)
	bond, err := node.fetchAssetMeta(ctx, bondId.String())
	if err != nil {
		return false, fmt.Errorf("node.fetchAssetMeta(%s) => %v", bondId.String(), err)
	}
	if bond != nil {
		return true, nil
	}
	return false, abi.GetOrDeployFactoryAsset(rpc, key, asset.AssetId, asset.Symbol, asset.Name, holder)
}

func (node *Node) fetchAssetMeta(ctx context.Context, id string) (*Asset, error) {
	meta, err := node.store.ReadAssetMeta(ctx, id)
	if err != nil || meta != nil {
		return meta, err
	}

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
	if err != nil || body.Data == nil {
		return nil, err
	}
	asset := body.Data

	var chain byte
	switch asset.ChainId {
	case keeper.SafeBitcoinChainId:
		chain = keeper.SafeChainBitcoin
	case keeper.SafeEthereumChainId:
		chain = keeper.SafeChainEthereum
	case keeper.SafeMVMChainId:
		chain = keeper.SafeChainMVM
	default:
		panic(asset.ChainId)
	}

	meta = &Asset{
		AssetId:   asset.AssetId,
		MixinId:   asset.MixinId.String(),
		AssetKey:  asset.AssetKey,
		Symbol:    asset.Symbol,
		Name:      asset.Name,
		Decimals:  asset.Precision,
		Chain:     chain,
		CreatedAt: time.Now().UTC(),
	}
	return meta, node.store.WriteAssetMeta(ctx, meta)
}