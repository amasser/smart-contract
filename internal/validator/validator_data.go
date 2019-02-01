package validator

import (
	"github.com/tokenized/smart-contract/internal/platform/state/contract"
	"github.com/tokenized/smart-contract/pkg/protocol"
)

type validatorData struct {
	contract *contract.Contract
	m        protocol.OpReturnMessage
}
