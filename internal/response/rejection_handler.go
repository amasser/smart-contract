package response

import (
	"context"

	"github.com/tokenized/smart-contract/internal/platform/inspector"
	"github.com/tokenized/smart-contract/internal/platform/state/contract"
)

type rejectionHandler struct{}

func newRejectionHandler() rejectionHandler {
	return rejectionHandler{}
}

func (h rejectionHandler) process(ctx context.Context,
	itx *inspector.Transaction, c *contract.Contract) error {

	return nil
}
