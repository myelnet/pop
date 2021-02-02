package deal

import datatransfer "github.com/filecoin-project/go-data-transfer"

// ProposalFromVoucher casts a data transfer voucher into a deal Proposal
func ProposalFromVoucher(voucher datatransfer.Voucher) (*Proposal, bool) {
	dealProposal, ok := voucher.(*Proposal)
	// if this event is for a transfer not related to storage, ignore
	if !ok {
		return nil, false
	}
	return dealProposal, true
}

// ResponseFromVoucherResult casts a data transfer voucher result into a deal Response
func ResponseFromVoucherResult(vres datatransfer.VoucherResult) (*Response, bool) {
	dealResponse, ok := vres.(*Response)
	// if this event is for a transfer not related to storage, ignore
	if !ok {
		return nil, false
	}
	return dealResponse, true
}
