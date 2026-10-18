package contracts

import (
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Days after the due date before an overdue receivable can be marked
// defaulted (the RBI 90-day NPA convention), and the MSMED Act payment limit.
const (
	defaultAfterDays = 90
	msmedPaymentDays = 45
	fundingWindow    = 24 * time.Hour
)

var fundingRails = map[string]bool{"IMPS": true, "UPI": true, "NEFT": true, "RTGS": true}
var mandateRails = map[string]bool{"E_NACH": true, "UPI_AUTOPAY": true}

// Settlement ties the ledger to money movement on NPCI and RBI rails using a
// two-phase pattern: chaincode emits a payment instruction, the bank adapter
// moves the money, then posts the UTR back. State changes only on the UTR.
type Settlement struct {
	contractapi.Contract
}

// ConfirmFunding is posted by a winning financier once its payment to the MSME
// has a UTR. The financier's units move to it; when every award is confirmed
// the receivable becomes FUNDED.
func (c *Settlement) ConfirmFunding(ctx contractapi.TransactionContextInterface,
	id, bidID, utr, rail string, amountPaise int64) (*Receivable, error) {
	caller, err := requireRole(ctx, RoleFinancier)
	if err != nil {
		return nil, err
	}
	if !fundingRails[rail] {
		return nil, fmt.Errorf("rail must be IMPS, UPI, NEFT or RTGS")
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := r.requireStatus(StatusFundingPending); err != nil {
		return nil, err
	}
	var award *Award
	for i := range r.Awards {
		if r.Awards[i].BidID == bidID {
			award = &r.Awards[i]
		}
	}
	if award == nil || award.FinancierMSP != caller.MSP {
		return nil, fmt.Errorf("no award for bid %s to %s", bidID, caller.MSP)
	}
	if award.Confirmed {
		return nil, fmt.Errorf("funding for bid %s is already confirmed", bidID)
	}
	if amountPaise != award.FundingPaise {
		return nil, fmt.Errorf("paid %d paise, award requires %d", amountPaise, award.FundingPaise)
	}
	if err := claimUTR(ctx, utr, id); err != nil {
		return nil, err
	}
	if err := moveUnits(ctx, id, r.SupplierID, award.FinancierID, award.Units); err != nil {
		return nil, err
	}
	award.Confirmed, award.FundingUTR, award.Rail = true, utr, rail
	r.FundedPaise += amountPaise

	allDone := true
	for _, a := range r.Awards {
		allDone = allDone && a.Confirmed
	}
	event := "FundingConfirmed"
	if allDone {
		if err := r.moveTo(StatusFunded); err != nil {
			return nil, err
		}
		event = "ReceivableFunded"
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, event, map[string]any{"id": id, "bidId": bidID, "utr": utr, "fundedPaise": r.FundedPaise})
}

// ExpireUnfundedAwards releases awards whose financier has not paid within
// 24 hours of the award. If some awards were funded the receivable becomes
// FUNDED and the supplier keeps the unsold units; if none were, it goes back
// to LISTED for fresh bids.
func (c *Settlement) ExpireUnfundedAwards(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	caller, err := requireRole(ctx, RoleSupplier, RoleOperator)
	if err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	if caller.Role == RoleSupplier && caller.PartyID != r.SupplierID {
		return nil, fmt.Errorf("caller is not the supplier on receivable %s", id)
	}
	if err := r.requireStatus(StatusFundingPending); err != nil {
		return nil, err
	}
	awardedAt, err := time.Parse(time.RFC3339, r.AwardedAt)
	if err != nil {
		return nil, err
	}
	now, err := txTime(ctx)
	if err != nil {
		return nil, err
	}
	if now.Sub(awardedAt) < fundingWindow {
		return nil, fmt.Errorf("financiers have until %s to fund", awardedAt.Add(fundingWindow).Format(time.RFC3339))
	}
	var kept []Award
	var lapsed []string
	for _, a := range r.Awards {
		if a.Confirmed {
			kept = append(kept, a)
			continue
		}
		commit, key, err := getBid(ctx, id, a.BidID)
		if err != nil {
			return nil, err
		}
		commit.Status = "LAPSED"
		if err := putJSON(ctx, key, commit); err != nil {
			return nil, err
		}
		lapsed = append(lapsed, a.BidID)
	}
	r.Awards = kept
	next := StatusFunded
	if len(kept) == 0 {
		next, r.AwardedAt = StatusListed, ""
	}
	if err := r.moveTo(next); err != nil {
		return nil, err
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, "AwardsLapsed", map[string]any{"id": id, "lapsedBids": lapsed, "status": r.Status})
}

// RegisterMandate records the buyer's e-NACH or UPI AutoPay mandate that will
// collect the receivable on its due date.
func (c *Settlement) RegisterMandate(ctx contractapi.TransactionContextInterface, id, mandateRef, rail string) error {
	r, err := buyerAction(ctx, id)
	if err != nil {
		return err
	}
	if !mandateRails[rail] {
		return fmt.Errorf("rail must be E_NACH or UPI_AUTOPAY")
	}
	if err := r.requireStatus(StatusAccepted, StatusListed, StatusFundingPending, StatusFunded); err != nil {
		return err
	}
	r.MandateRef, r.MandateRail = mandateRef, rail
	if err := putReceivable(ctx, r); err != nil {
		return err
	}
	return emit(ctx, "MandateRegistered", map[string]any{"id": id, "rail": rail, "dueDate": r.DueDate})
}

// ConfirmRepayment records a buyer payment (full or partial) by its UTR and
// splits it pro-rata across current unit holders as payout instructions.
// Callable by the buyer or by the operator's collection adapter.
func (c *Settlement) ConfirmRepayment(ctx contractapi.TransactionContextInterface, id, utr string, amountPaise int64) (*Receivable, error) {
	caller, err := requireRole(ctx, RoleBuyer, RoleOperator)
	if err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	if caller.Role == RoleBuyer && caller.PartyID != r.BuyerID {
		return nil, fmt.Errorf("caller is not the buyer on receivable %s", id)
	}
	if err := r.requireStatus(StatusFunded, StatusOverdue, StatusDefaulted); err != nil {
		return nil, err
	}
	outstanding := r.AmountPaise - r.RepaidPaise
	if amountPaise <= 0 || amountPaise > outstanding {
		return nil, fmt.Errorf("repayment must be 1-%d paise", outstanding)
	}
	if err := claimUTR(ctx, utr, id); err != nil {
		return nil, err
	}
	holdings, err := listHoldings(ctx, id)
	if err != nil {
		return nil, err
	}
	shares := ProRata(amountPaise, holdings)
	txID := ctx.GetStub().GetTxID()
	var payouts []Payout
	for i, h := range holdings {
		if shares[i] == 0 {
			continue
		}
		payouts = append(payouts, Payout{
			PayoutID: txID + "-" + strconv.Itoa(i), HolderID: h.HolderID,
			AmountPaise: shares[i], RepaymentUTR: utr,
		})
	}
	r.Payouts = append(r.Payouts, payouts...)
	r.RepaidPaise += amountPaise

	today, _ := txDate(ctx)
	if err := r.updateLateness(today); err != nil {
		return nil, err
	}
	if r.RepaidPaise == r.AmountPaise {
		if err := r.moveTo(StatusRepaid); err != nil {
			return nil, err
		}
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, "RepaymentReceived", map[string]any{
		"id": id, "utr": utr, "amountPaise": amountPaise, "status": r.Status, "payouts": payouts,
	})
}

// ProRata splits amount across holdings by units. Shares round down and the
// leftover paise go to the largest holder (first one on a tie), so the parts
// always add up to the amount exactly.
func ProRata(amount int64, holdings []Holding) []int64 {
	shares := make([]int64, len(holdings))
	var total, allocated int64
	largest := 0
	for i, h := range holdings {
		total += h.Units
		if h.Units > holdings[largest].Units {
			largest = i
		}
	}
	if total == 0 {
		return shares
	}
	for i, h := range holdings {
		s := new(big.Int).Mul(big.NewInt(amount), big.NewInt(h.Units))
		s.Quo(s, big.NewInt(total))
		shares[i] = s.Int64()
		allocated += shares[i]
	}
	shares[largest] += amount - allocated
	return shares
}

// ConfirmPayout records the UTR of a payout to a holder. Once the receivable
// is fully repaid and every payout is confirmed, it is CLOSED.
func (c *Settlement) ConfirmPayout(ctx contractapi.TransactionContextInterface, id, payoutID, utr string) (*Receivable, error) {
	if _, err := requireRole(ctx, RoleOperator); err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	var p *Payout
	for i := range r.Payouts {
		if r.Payouts[i].PayoutID == payoutID {
			p = &r.Payouts[i]
		}
	}
	if p == nil {
		return nil, fmt.Errorf("payout %s not found on %s", payoutID, id)
	}
	if p.Confirmed {
		return nil, fmt.Errorf("payout %s already confirmed", payoutID)
	}
	if err := claimUTR(ctx, utr, id); err != nil {
		return nil, err
	}
	p.Confirmed, p.PayoutUTR = true, utr

	allPaid := true
	for _, q := range r.Payouts {
		allPaid = allPaid && q.Confirmed
	}
	event := "PayoutConfirmed"
	if r.Status == StatusRepaid && allPaid {
		if err := r.moveTo(StatusClosed); err != nil {
			return nil, err
		}
		event = "ReceivableClosed"
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, event, map[string]any{"id": id, "payoutId": payoutID, "utr": utr})
}

// MarkOverdue moves a funded receivable past its due date to OVERDUE.
// Anyone may call it; the check uses the transaction date.
func (c *Settlement) MarkOverdue(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	if _, err := getCaller(ctx); err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	today, err := txDate(ctx)
	if err != nil {
		return nil, err
	}
	due, _ := parseDate(r.DueDate)
	if !today.After(due) {
		return nil, fmt.Errorf("receivable %s is not past its due date %s", id, r.DueDate)
	}
	if err := r.moveTo(StatusOverdue); err != nil {
		return nil, err
	}
	if err := r.updateLateness(today); err != nil {
		return nil, err
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, "ReceivableOverdue", map[string]any{
		"id": id, "daysLate": r.DaysLate, "breach45Days": r.Breach45Days,
	})
}

// MarkDefaulted moves an overdue receivable to DEFAULTED 90 days after due.
func (c *Settlement) MarkDefaulted(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	if _, err := requireRole(ctx, RoleOperator); err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	today, err := txDate(ctx)
	if err != nil {
		return nil, err
	}
	due, _ := parseDate(r.DueDate)
	if daysBetween(due, today) <= defaultAfterDays {
		return nil, fmt.Errorf("receivable %s is not %d days past due", id, defaultAfterDays)
	}
	if err := r.moveTo(StatusDefaulted); err != nil {
		return nil, err
	}
	if err := r.updateLateness(today); err != nil {
		return nil, err
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, "ReceivableDefaulted", map[string]any{"id": id, "daysLate": r.DaysLate})
}

// updateLateness records days past due and whether the buyer has gone beyond
// the MSMED Act 45-day limit (relevant to the buyer's Section 43B(h) deduction).
func (r *Receivable) updateLateness(today time.Time) error {
	due, err := parseDate(r.DueDate)
	if err != nil {
		return err
	}
	inv, err := parseDate(r.InvoiceDate)
	if err != nil {
		return err
	}
	if d := daysBetween(due, today); d > 0 {
		r.DaysLate = d
	}
	if daysBetween(inv, today) > msmedPaymentDays {
		r.Breach45Days = true
	}
	return nil
}
