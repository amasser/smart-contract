package handlers

import (
	"context"
	"fmt"
	"time"

	"github.com/tokenized/pkg/bitcoin"
	"github.com/tokenized/smart-contract/internal/contract"
	"github.com/tokenized/smart-contract/internal/platform/db"
	"github.com/tokenized/smart-contract/internal/platform/node"
	"github.com/tokenized/smart-contract/internal/platform/state"
	"github.com/tokenized/smart-contract/internal/transactions"
	"github.com/tokenized/smart-contract/internal/vote"
	"github.com/tokenized/smart-contract/pkg/inspector"
	"github.com/tokenized/smart-contract/pkg/wallet"

	"github.com/tokenized/specification/dist/golang/actions"
	"github.com/tokenized/specification/dist/golang/protocol"

	"github.com/pkg/errors"
	"go.opencensus.io/trace"
)

type Contract struct {
	MasterDB *db.DB
	Config   *node.Config
	Headers  node.BitcoinHeaders
}

// OfferRequest handles an incoming Contract Offer and prepares a Formation response
func (c *Contract) OfferRequest(ctx context.Context, w *node.ResponseWriter, itx *inspector.Transaction, rk *wallet.Key) error {
	ctx, span := trace.StartSpan(ctx, "handlers.Contract.Offer")
	defer span.End()

	msg, ok := itx.MsgProto.(*actions.ContractOffer)
	if !ok {
		return errors.New("Could not assert as *actions.ContractOffer")
	}

	v := ctx.Value(node.KeyValues).(*node.Values)

	// Validate all fields have valid values.
	if itx.RejectCode != 0 {
		node.LogWarn(ctx, "Contract offer request invalid : %d", itx.RejectCode)
		return node.RespondReject(ctx, w, itx, rk, itx.RejectCode)
	}

	// Locate Contract
	_, err := contract.Retrieve(ctx, c.MasterDB, rk.Address, c.Config.IsTest)
	if err != contract.ErrNotFound {
		if err == nil {
			address := bitcoin.NewAddressFromRawAddress(rk.Address, w.Config.Net)
			node.LogWarn(ctx, "Contract already exists : %s", address.String())
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsContractExists)
		} else {
			return errors.Wrap(err, "Failed to retrieve contract")
		}
	}

	if msg.BodyOfAgreementType == 1 && len(msg.BodyOfAgreement) != 32 {
		node.LogWarn(ctx, "Contract body of agreement hash is incorrect length : %d",
			len(msg.BodyOfAgreement))
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
	}

	if msg.ContractExpiration != 0 && msg.ContractExpiration < v.Now.Nano() {
		node.LogWarn(ctx, "Expiration already passed : %d", msg.ContractExpiration)
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
	}

	if len(msg.MasterAddress) > 0 {
		if _, err := bitcoin.DecodeRawAddress(msg.MasterAddress); err != nil {
			node.LogWarn(ctx, "Invalid master address : %s", err)
			return node.RespondRejectText(ctx, w, itx, rk, actions.RejectionsMsgMalformed,
				"invalid master address")
		}
	}

	if _, err = actions.PermissionsFromBytes(msg.ContractPermissions, len(msg.VotingSystems)); err != nil {
		node.LogWarn(ctx, "Invalid contract permissions : %s", err)
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
	}

	// Validate voting systems are all valid.
	for _, votingSystem := range msg.VotingSystems {
		if err = vote.ValidateVotingSystem(votingSystem); err != nil {
			node.LogWarn(ctx, "Invalid voting system : %s", err)
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}
	}

	node.Log(ctx, "Accepting contract offer : %s", msg.ContractName)

	// Contract Formation <- Contract Offer
	cf := &actions.ContractFormation{}

	err = node.Convert(ctx, &msg, cf)
	if err != nil {
		return err
	}

	cf.AdminAddress = itx.Inputs[0].Address.Bytes()
	if msg.ContractOperatorIncluded {
		if len(itx.Inputs) < 2 {
			node.LogWarn(ctx, "Missing operator input")
			return node.RespondRejectText(ctx, w, itx, rk, actions.RejectionsMsgMalformed,
				"missing operator input")
		}
		cf.OperatorAddress = itx.Inputs[1].Address.Bytes()
	}

	cf.ContractRevision = 0
	cf.Timestamp = v.Now.Nano()

	// Build outputs
	// 1 - Contract Address
	// 2 - Contract Fee (change)
	w.AddOutput(ctx, rk.Address, 0)
	w.AddContractFee(ctx, msg.ContractFee)

	// Save Tx for when formation is processed.
	if err := transactions.AddTx(ctx, c.MasterDB, itx); err != nil {
		return errors.Wrap(err, "Failed to save tx")
	}

	// Respond with a formation
	if err := node.RespondSuccess(ctx, w, itx, rk, cf); err == nil {
		return contract.SaveContractFormation(ctx, c.MasterDB, rk.Address, cf, c.Config.IsTest)
	}
	return err
}

// AmendmentRequest handles an incoming Contract Amendment and prepares a Formation response
func (c *Contract) AmendmentRequest(ctx context.Context, w *node.ResponseWriter,
	itx *inspector.Transaction, rk *wallet.Key) error {

	ctx, span := trace.StartSpan(ctx, "handlers.Contract.Amendment")
	defer span.End()

	msg, ok := itx.MsgProto.(*actions.ContractAmendment)
	if !ok {
		return errors.New("Could not assert as *protocol.ContractAmendment")
	}

	v := ctx.Value(node.KeyValues).(*node.Values)

	// Validate all fields have valid values.
	if itx.RejectCode != 0 {
		node.LogWarn(ctx, "Contract amendment request invalid")
		return node.RespondReject(ctx, w, itx, rk, itx.RejectCode)
	}

	// Locate Contract
	ct, err := contract.Retrieve(ctx, c.MasterDB, rk.Address, c.Config.IsTest)
	if err != nil {
		return errors.Wrap(err, "Failed to retrieve contract")
	}

	if !ct.MovedTo.IsEmpty() {
		address := bitcoin.NewAddressFromRawAddress(ct.MovedTo, w.Config.Net)
		node.LogWarn(ctx, "Contract address changed : %s", address.String())
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsContractMoved)
	}

	if !contract.IsOperator(ctx, ct, itx.Inputs[0].Address) {
		address := bitcoin.NewAddressFromRawAddress(itx.Inputs[0].Address, w.Config.Net)
		node.LogVerbose(ctx, "Requestor is not operator : %s", address.String())
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsNotOperator)
	}

	if ct.Revision != msg.ContractRevision {
		node.LogWarn(ctx, "Incorrect contract revision : specified %d != current %d",
			msg.ContractRevision, ct.Revision)
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsContractRevision)
	}

	// Check proposal if there was one
	proposed := false
	proposalType := uint32(0)
	votingSystem := uint32(0)

	if len(msg.RefTxID) != 0 { // Vote Result Action allowing these amendments
		proposed = true

		refTxId, err := bitcoin.NewHash32(msg.RefTxID)
		if err != nil {
			return errors.Wrap(err, "Failed to convert protocol.TxId to Hash32")
		}

		// Retrieve Vote Result
		voteResultTx, err := transactions.GetTx(ctx, c.MasterDB, refTxId, c.Config.IsTest)
		if err != nil {
			node.LogWarn(ctx, "Vote Result tx not found for amendment")
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}

		voteResult, ok := voteResultTx.MsgProto.(*actions.Result)
		if !ok {
			node.LogWarn(ctx, "Vote Result invalid for amendment")
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}

		// Retrieve the vote
		voteTxId := protocol.TxIdFromBytes(voteResult.VoteTxId)
		vt, err := vote.Retrieve(ctx, c.MasterDB, rk.Address, voteTxId)
		if err == vote.ErrNotFound {
			node.LogWarn(ctx, "Vote not found : %s", voteTxId.String())
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsVoteNotFound)
		} else if err != nil {
			node.LogWarn(ctx, "Failed to retrieve vote : %s : %s", voteTxId.String(), err)
			return errors.Wrap(err, "Failed to retrieve vote")
		}

		if vt.CompletedAt.Nano() == 0 {
			node.LogWarn(ctx, "Vote not complete yet")
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}

		if vt.Result != "A" {
			node.LogWarn(ctx, "Vote result not A(Accept) : %s", vt.Result)
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}

		if len(vt.ProposedAmendments) == 0 {
			node.LogWarn(ctx, "Vote was not for specific amendments")
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}

		if !vt.AssetCode.IsZero() {
			node.LogWarn(ctx, "Vote was not for contract amendments")
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}

		// Verify proposal amendments match these amendments.
		if len(voteResult.ProposedAmendments) != len(msg.Amendments) {
			node.LogWarn(ctx, "%s : Proposal has different count of amendments : %d != %d",
				v.TraceID, len(voteResult.ProposedAmendments), len(msg.Amendments))
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
		}

		for i, amendment := range voteResult.ProposedAmendments {
			if !amendment.Equal(msg.Amendments[i]) {
				node.LogWarn(ctx, "Proposal amendment %d doesn't match", i)
				return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
			}
		}

		proposalType = vt.Type
		votingSystem = vt.VoteSystem
	}

	// Contract Formation <- Contract Amendment
	cf, err := contract.FetchContractFormation(ctx, c.MasterDB, rk.Address, c.Config.IsTest)
	if err != nil {
		return errors.Wrap(err, "fetch contract formation")
	}

	// Ensure reduction in qty is OK, keeping in mind that zero (0) means
	// unlimited asset creation is permitted.
	if cf.RestrictedQtyAssets > 0 && cf.RestrictedQtyAssets < uint64(len(ct.AssetCodes)) {
		node.LogWarn(ctx, "Cannot reduce allowable assets below existing number")
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsContractAssetQtyReduction)
	}

	if msg.ChangeAdministrationAddress || msg.ChangeOperatorAddress {
		if !ct.OperatorAddress.IsEmpty() {
			if len(itx.Inputs) < 2 {
				node.Log(ctx, "All operators required for operator change")
				return node.RespondReject(ctx, w, itx, rk,
					actions.RejectionsContractBothOperatorsRequired)
			}

			if itx.Inputs[0].Address.Equal(itx.Inputs[1].Address) ||
				!contract.IsOperator(ctx, ct, itx.Inputs[0].Address) ||
				!contract.IsOperator(ctx, ct, itx.Inputs[1].Address) {
				node.Log(ctx, "All operators required for operator change")
				return node.RespondReject(ctx, w, itx, rk,
					actions.RejectionsContractBothOperatorsRequired)
			}
		} else {
			if len(itx.Inputs) < 1 {
				node.Log(ctx, "All operators required for operator change")
				return node.RespondReject(ctx, w, itx, rk,
					actions.RejectionsContractBothOperatorsRequired)
			}

			if !contract.IsOperator(ctx, ct, itx.Inputs[0].Address) {
				node.Log(ctx, "All operators required for operator change")
				return node.RespondReject(ctx, w, itx, rk,
					actions.RejectionsContractBothOperatorsRequired)
			}
		}
	}

	// Pull from amendment tx.
	// Administration change. New administration in second input
	inputIndex := 1
	if !ct.OperatorAddress.IsEmpty() {
		inputIndex++
	}

	if msg.ChangeAdministrationAddress {
		if len(itx.Inputs) <= inputIndex {
			return errors.New("New administration specified but not included in inputs")
		}

		cf.AdminAddress = itx.Inputs[inputIndex].Address.Bytes()
		inputIndex++
	}

	// Operator changes. New operator in second input unless there is also a new administration,
	// then it is in the third input
	if msg.ChangeOperatorAddress {
		if len(itx.Inputs) <= inputIndex {
			return errors.New("New operator specified but not included in inputs")
		}

		cf.OperatorAddress = itx.Inputs[inputIndex].Address.Bytes()
	}

	if err := applyContractAmendments(cf, msg.Amendments, proposed, proposalType,
		votingSystem); err != nil {
		node.LogWarn(ctx, "Failed to apply amendments to check admin oracle sig : %s", err)
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsMsgMalformed)
	}

	// Check admin identity oracle signatures
	for _, adminCert := range cf.AdminIdentityCertificates {
		if err := validateContractAmendOracleSig(ctx, c.MasterDB, cf, adminCert, c.Headers,
			c.Config.IsTest); err != nil {
			node.LogVerbose(ctx, "New admin identity signature invalid : %s", err)
			return node.RespondReject(ctx, w, itx, rk, actions.RejectionsInvalidSignature)
		}
	}

	// Apply modifications
	cf.ContractRevision = ct.Revision + 1 // Bump the revision
	cf.Timestamp = v.Now.Nano()

	// Build outputs
	// 1 - Contract Address
	// 2 - Contract Fee (change)
	w.AddOutput(ctx, rk.Address, 0)
	w.AddContractFee(ctx, ct.ContractFee)

	// Save Tx.
	if err := transactions.AddTx(ctx, c.MasterDB, itx); err != nil {
		return errors.Wrap(err, "Failed to save tx")
	}

	node.Log(ctx, "Accepting contract amendment")

	// Respond with a formation
	if err := node.RespondSuccess(ctx, w, itx, rk, cf); err == nil {
		return contract.SaveContractFormation(ctx, c.MasterDB, rk.Address, cf, c.Config.IsTest)
	}
	return err
}

// FormationResponse handles an outgoing Contract Formation and writes it to the state
func (c *Contract) FormationResponse(ctx context.Context, w *node.ResponseWriter,
	itx *inspector.Transaction, rk *wallet.Key) error {

	ctx, span := trace.StartSpan(ctx, "handlers.Contract.Formation")
	defer span.End()

	msg, ok := itx.MsgProto.(*actions.ContractFormation)
	if !ok {
		return errors.New("Could not assert as *actions.ContractFormation")
	}

	v := ctx.Value(node.KeyValues).(*node.Values)
	if itx.RejectCode != 0 {
		return errors.New("Contract formation response invalid")
	}

	// Locate Contract. Sender is verified to be contract before this response function is called.
	if !itx.Inputs[0].Address.Equal(rk.Address) {
		return fmt.Errorf("Contract formation not from contract : %x",
			itx.Inputs[0].Address.Bytes())
	}

	contractName := msg.ContractName
	ct, err := contract.Retrieve(ctx, c.MasterDB, rk.Address, c.Config.IsTest)
	if err != nil && err != contract.ErrNotFound {
		return errors.Wrap(err, "Failed to retrieve contract")
	}

	if ct != nil && !ct.MovedTo.IsEmpty() {
		address := bitcoin.NewAddressFromRawAddress(ct.MovedTo, w.Config.Net)
		return fmt.Errorf("Contract address changed : %s", address.String())
	}

	// Get request tx
	request, err := transactions.GetTx(ctx, c.MasterDB, &itx.Inputs[0].UTXO.Hash, c.Config.IsTest)
	var vt *state.Vote
	var amendment *actions.ContractAmendment
	if err == nil && request != nil {
		var ok bool
		amendment, ok = request.MsgProto.(*actions.ContractAmendment)

		if ok && len(amendment.RefTxID) != 0 {
			refTxId, err := bitcoin.NewHash32(amendment.RefTxID)
			if err != nil {
				return errors.Wrap(err, "Failed to convert protocol.TxId to bitcoin.Hash32")
			}

			// Retrieve Vote Result
			voteResultTx, err := transactions.GetTx(ctx, c.MasterDB, refTxId, c.Config.IsTest)
			if err != nil {
				return errors.New("Vote Result tx not found for amendment")
			}

			voteResult, ok := voteResultTx.MsgProto.(*actions.Result)
			if !ok {
				return errors.New("Vote Result invalid for amendment")
			}

			// Retrieve the vote
			voteTxId := protocol.TxIdFromBytes(voteResult.VoteTxId)
			vt, err = vote.Retrieve(ctx, c.MasterDB, rk.Address, voteTxId)
			if err == vote.ErrNotFound {
				return errors.New("Vote not found for amendment")
			} else if err != nil {
				return errors.New("Failed to retrieve vote for amendment")
			}
		}
	}

	// Create or update Contract
	if ct == nil {
		// Prepare creation object
		var nc contract.NewContract
		err := node.Convert(ctx, &msg, &nc)
		if err != nil {
			node.LogWarn(ctx, "Failed to convert formation to new contract (%s) : %s", contractName, err.Error())
			return err
		}

		// Get contract offer message to retrieve administration and operator.
		var offerTx *inspector.Transaction
		offerTx, err = transactions.GetTx(ctx, c.MasterDB, &itx.Inputs[0].UTXO.Hash, c.Config.IsTest)
		if err != nil {
			return errors.Wrap(err, fmt.Sprintf("Contract Offer tx not found : %s", itx.Inputs[0].UTXO.Hash.String()))
		}

		// Get offer from it
		offer, ok := offerTx.MsgProto.(*actions.ContractOffer)
		if !ok {
			return fmt.Errorf("Could not find Contract Offer in offer tx")
		}

		nc.AdminAddress = offerTx.Inputs[0].Address // First input of offer tx
		if offer.ContractOperatorIncluded && len(offerTx.Inputs) > 1 {
			nc.OperatorAddress = offerTx.Inputs[1].Address // Second input of offer tx
		}

		if err := contract.Create(ctx, c.MasterDB, rk.Address, &nc, c.Config.IsTest, v.Now); err != nil {
			node.LogWarn(ctx, "Failed to create contract (%s) : %s", contractName, err)
			return err
		}
		node.Log(ctx, "Created contract (%s)", contractName)
	} else {
		// Prepare update object
		ts := protocol.NewTimestamp(msg.Timestamp)
		uc := contract.UpdateContract{
			Revision:  &msg.ContractRevision,
			Timestamp: &ts,
		}

		// Pull from amendment tx.
		// Administration change. New administration in next input
		inputIndex := 1
		if !ct.OperatorAddress.IsEmpty() {
			inputIndex++
		}
		if amendment != nil && amendment.ChangeAdministrationAddress {
			if len(request.Inputs) <= inputIndex {
				return errors.New("New administration specified but not included in inputs")
			}

			uc.AdminAddress = &request.Inputs[inputIndex].Address
			inputIndex++
			address := bitcoin.NewAddressFromRawAddress(*uc.AdminAddress, w.Config.Net)
			node.Log(ctx, "Updating contract administration address : %s", address.String())
		}

		// Operator changes. New operator in second input unless there is also a new administration, then it is in the third input
		if amendment != nil && amendment.ChangeOperatorAddress {
			if len(request.Inputs) <= inputIndex {
				return errors.New("New operator specified but not included in inputs")
			}

			uc.OperatorAddress = &request.Inputs[inputIndex].Address
			address := bitcoin.NewAddressFromRawAddress(*uc.OperatorAddress, w.Config.Net)
			node.Log(ctx, "Updating contract operator PKH : %s", address.String())
		}

		if ct.ContractType != msg.ContractType {
			uc.ContractType = &msg.ContractType
			node.Log(ctx, "Updating contract type : %d", *uc.ContractType)
		}

		if ct.ContractFee != msg.ContractFee {
			uc.ContractFee = &msg.ContractFee
			node.Log(ctx, "Updating contract fee : %d", *uc.ContractFee)
		}

		if ct.ContractExpiration.Nano() != msg.ContractExpiration {
			ts := protocol.NewTimestamp(msg.ContractExpiration)
			uc.ContractExpiration = &ts
			newExpiration := time.Unix(int64(msg.ContractExpiration), 0)
			node.Log(ctx, "Updating contract expiration : %s", newExpiration.Format(time.UnixDate))
		}

		if ct.RestrictedQtyAssets != msg.RestrictedQtyAssets {
			uc.RestrictedQtyAssets = &msg.RestrictedQtyAssets
			node.Log(ctx, "Updating contract restricted quantity assets : %d",
				*uc.RestrictedQtyAssets)
		}

		if ct.AdministrationProposal != msg.AdministrationProposal {
			uc.AdministrationProposal = &msg.AdministrationProposal
			node.Log(ctx, "Updating contract administration proposal : %t",
				*uc.AdministrationProposal)
		}

		if ct.HolderProposal != msg.HolderProposal {
			uc.HolderProposal = &msg.HolderProposal
			node.Log(ctx, "Updating contract holder proposal : %t", *uc.HolderProposal)
		}

		// Check if oracles are different
		different := len(ct.Oracles) != len(msg.Oracles)
		if !different {
			for i, oracle := range ct.Oracles {
				if !oracle.Equal(msg.Oracles[i]) {
					different = true
					break
				}
			}
		}

		if different {
			node.Log(ctx, "Updating contract oracles")
			uc.Oracles = &msg.Oracles
		}

		// Check if voting systems are different
		different = len(ct.VotingSystems) != len(msg.VotingSystems)
		if !different {
			for i, votingSystem := range ct.VotingSystems {
				if !votingSystem.Equal(msg.VotingSystems[i]) {
					different = true
					break
				}
			}
		}

		if different {
			node.Log(ctx, "Updating contract voting systems")
			uc.VotingSystems = &msg.VotingSystems
		}

		if err := contract.Update(ctx, c.MasterDB, rk.Address, &uc, c.Config.IsTest, v.Now); err != nil {
			return errors.Wrap(err, "Failed to update contract")
		}
		node.Log(ctx, "Updated contract")

		// Mark vote as "applied" if this amendment was a result of a vote.
		if vt != nil {
			node.Log(ctx, "Marking vote as applied : %s", vt.VoteTxId.String())
			if err := vote.MarkApplied(ctx, c.MasterDB, rk.Address, vt.VoteTxId,
				protocol.TxIdFromBytes(request.Hash[:]), v.Now); err != nil {
				return errors.Wrap(err, "Failed to mark vote applied")
			}
		}
	}

	return nil
}

// AddressChange handles an incoming Contract Address Change.
func (c *Contract) AddressChange(ctx context.Context, w *node.ResponseWriter, itx *inspector.Transaction, rk *wallet.Key) error {
	ctx, span := trace.StartSpan(ctx, "handlers.Contract.AddressChange")
	defer span.End()

	msg, ok := itx.MsgProto.(*actions.ContractAddressChange)
	if !ok {
		return errors.New("Could not assert as *actions.ContractAddressChange")
	}

	// Validate all fields have valid values.
	if itx.RejectCode != 0 {
		node.LogWarn(ctx, "Contract address change request invalid")
		return node.RespondReject(ctx, w, itx, rk, itx.RejectCode)
	}

	// Locate Contract
	ct, err := contract.Retrieve(ctx, c.MasterDB, rk.Address, c.Config.IsTest)
	if err != nil && err != contract.ErrNotFound {
		return errors.Wrap(err, "Failed to retrieve contract")
	}

	// Check that it is from the master PKH
	if !itx.Inputs[0].Address.Equal(ct.MasterAddress) {
		address := bitcoin.NewAddressFromRawAddress(itx.Inputs[0].Address, w.Config.Net)
		node.LogWarn(ctx, "Contract address change must be from master address : %s", address.String())
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsTxMalformed)
	}

	newContractAddress, err := bitcoin.DecodeRawAddress(msg.NewContractAddress)
	if err != nil {
		node.LogWarn(ctx, "Invalid new contract address : %x", msg.NewContractAddress)
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsTxMalformed)
	}

	// Check that it is to the current contract address and the new contract address
	toCurrent := false
	toNew := false
	for _, output := range itx.Outputs {
		if output.Address.Equal(rk.Address) {
			toCurrent = true
		}
		if output.Address.Equal(newContractAddress) {
			toNew = true
		}
	}

	if !toCurrent || !toNew {
		node.LogWarn(ctx, "Contract address change must be to current and new PKH")
		return node.RespondReject(ctx, w, itx, rk, actions.RejectionsTxMalformed)
	}

	// Perform move
	err = contract.Move(ctx, c.MasterDB, rk.Address, newContractAddress, c.Config.IsTest,
		protocol.NewTimestamp(msg.Timestamp))
	if err != nil {
		return err
	}

	//TODO Transfer all UTXOs to fee address.

	return nil
}

// applyContractAmendments applies the amendments to the contract formation.
func applyContractAmendments(cf *actions.ContractFormation, amendments []*actions.AmendmentField,
	proposed bool, proposalType, votingSystem uint32) error {

	permissions, err := actions.PermissionsFromBytes(cf.ContractPermissions, len(cf.VotingSystems))
	if err != nil {
		return fmt.Errorf("Invalid contract permissions : %s", err)
	}

	for i, amendment := range amendments {
		fip, err := actions.FieldIndexPathFromBytes(amendment.FieldIndexPath)
		if err != nil {
			return fmt.Errorf("Failed to read amendment %d field index path : %s", i, err)
		}
		if len(fip) == 0 {
			return fmt.Errorf("Amendment %d has no field specified", i)
		}

		switch fip[0] {
		case actions.ContractFieldContractPermissions:
			if _, err := actions.PermissionsFromBytes(amendment.Data, len(cf.VotingSystems)); err != nil {
				return fmt.Errorf("ContractPermissions amendment value is invalid : %s", err)
			}

		case actions.ContractFieldMasterAddress:
			return node.NewError(actions.RejectionsContractPermissions,
				"MasterAddress amendments prohibited")

		case actions.ContractFieldContractRevision:
			return node.NewError(actions.RejectionsContractPermissions,
				"Revision amendments prohibited")

		case actions.ContractFieldTimestamp:
			return node.NewError(actions.RejectionsContractPermissions,
				"Timestamp amendments prohibited")

		case actions.ContractFieldAdminAddress:
			return node.NewError(actions.RejectionsContractPermissions,
				"AdminAddress amendments prohibited")

		case actions.ContractFieldOperatorAddress:
			return node.NewError(actions.RejectionsContractPermissions,
				"OperatorAddress amendments prohibited")
		}

		adjustedFIP, err := cf.ApplyAmendment(fip, amendment.Operation, amendment.Data)
		if err != nil {
			return err
		}

		// adjustedFIP has element indexes removed, which is how permissions are specified.
		permission := permissions.PermissionOf(adjustedFIP)
		if proposed {
			switch proposalType {
			case 0: // Administration
				if !permission.AdministrationProposal {
					return node.NewError(actions.RejectionsContractPermissions,
						fmt.Sprintf("Field %s amendment not permitted by administration proposal",
							adjustedFIP))
				}
			case 1: // Holder
				if !permission.HolderProposal {
					return node.NewError(actions.RejectionsContractPermissions,
						fmt.Sprintf("Field %s amendment not permitted by holder proposal", adjustedFIP))
				}
			case 2: // Administrative Matter
				if !permission.AdministrativeMatter {
					return node.NewError(actions.RejectionsContractPermissions,
						fmt.Sprintf("Field %s amendment not permitted by administrative vote", adjustedFIP))
				}
			default:
				return fmt.Errorf("Invalid proposal initiator type : %d", proposalType)
			}

			if int(votingSystem) >= len(permission.VotingSystemsAllowed) {
				return fmt.Errorf("Field %s amendment voting system out of range : %d", adjustedFIP,
					votingSystem)
			}
			if !permission.VotingSystemsAllowed[votingSystem] {
				return node.NewError(actions.RejectionsContractPermissions,
					fmt.Sprintf("Field %s amendment not allowed using voting system %d", adjustedFIP,
						votingSystem))
			}
		} else if !permission.Permitted {
			return node.NewError(actions.RejectionsContractPermissions,
				fmt.Sprintf("Field %s amendment not permitted without proposal", adjustedFIP))
		}
	}

	// Check validity of updated contract data
	if err := cf.Validate(); err != nil {
		return fmt.Errorf("Contract data invalid after amendments : %s", err)
	}

	return nil
}

func validateContractAmendOracleSig(ctx context.Context, dbConn *db.DB,
	cf *actions.ContractFormation, adminCert *actions.AdminIdentityCertificateField,
	headers node.BitcoinHeaders, isTest bool) error {

	// EntityContract       []byte   `protobuf:"bytes,1,opt,name=EntityContract,proto3" json:"EntityContract,omitempty"`
	// Signature            []byte   `protobuf:"bytes,2,opt,name=Signature,proto3" json:"Signature,omitempty"`
	// BlockHeight          uint32   `protobuf:"varint,3,opt,name=BlockHeight,proto3" json:"BlockHeight,omitempty"`
	// Expiration           uint64   `protobuf:"varint,4,opt,name=Expiration,proto3" json:"Expiration,omitempty"`

	oracleAddress, err := bitcoin.DecodeRawAddress(adminCert.EntityContract)
	if err != nil {
		return errors.Wrap(err, "entity address")
	}

	oracleContract, err := contract.FetchContractFormation(ctx, dbConn, oracleAddress, isTest)
	if err != nil {
		return errors.Wrap(err, "fetch oracle")
	}

	oracle, err := contract.GetIdentityOracleKey(oracleContract)
	if err != nil {
		return errors.Wrap(err, "get identity oracle")
	}

	// Parse signature
	oracleSig, err := bitcoin.SignatureFromBytes(adminCert.Signature)
	if err != nil {
		return errors.Wrap(err, "Failed to parse oracle signature")
	}

	hash, err := headers.Hash(ctx, int(adminCert.BlockHeight))
	if err != nil {
		return errors.Wrap(err, fmt.Sprintf("Failed to retrieve hash for block height %d",
			adminCert.BlockHeight))
	}

	addresses := make([]bitcoin.RawAddress, 0, 2)
	entities := make([]*actions.EntityField, 0, 2)

	adminAddress, err := bitcoin.DecodeRawAddress(cf.AdminAddress)
	if err != nil {
		return errors.Wrap(err, "admin address")
	}

	addresses = append(addresses, adminAddress)
	entities = append(entities, cf.Issuer)

	if len(cf.OperatorAddress) != 0 {
		operatorAddress, err := bitcoin.DecodeRawAddress(cf.OperatorAddress)
		if err != nil {
			return errors.Wrap(err, "operator address")
		}

		addresses = append(addresses, operatorAddress)

		operatorContract, err := contract.FetchContractFormation(ctx, dbConn, operatorAddress,
			isTest)
		if err != nil {
			return errors.Wrap(err, "fetch operator")
		}
		entities = append(entities, operatorContract.Issuer)
	}

	sigHash, err := protocol.ContractOracleSigHash(ctx, addresses, entities, hash, 1)
	if err != nil {
		return err
	}

	if oracleSig.Verify(sigHash, oracle) {
		return nil // Valid signature found
	}

	return fmt.Errorf("Contract signature invalid")
}
