package response

import (
	"context"

	"github.com/tokenized/smart-contract/internal/platform/inspector"
	"github.com/tokenized/smart-contract/internal/platform/state/contract"
	"github.com/tokenized/smart-contract/pkg/protocol"
)

type assetCreationHandler struct{}

func newAssetCreationHandler() assetCreationHandler {
	return assetCreationHandler{}
}

func (h assetCreationHandler) process(ctx context.Context,
	itx *inspector.Transaction, c *contract.Contract) error {

	if len(itx.Outputs) < 2 {
		return nil
	}

	msg := itx.MsgProto.(*protocol.AssetCreation)
	assetKey := string(msg.AssetID)

	asset, ok := c.Assets[assetKey]
	if !ok {
		issuerAddr := itx.Outputs[1].Address.String()
		holding := contract.NewHolding(issuerAddr, msg.Qty)
		asset = contract.NewAsset(msg, holding)
	} else {
		asset = contract.EditAsset(asset, msg)
	}

	c.Assets[asset.ID] = asset

	return nil
}
