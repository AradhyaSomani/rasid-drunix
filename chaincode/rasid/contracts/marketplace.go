package contracts

import (
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Transient map keys used by the Marketplace.
const (
	transientBid    = "bid"
	transientAwards = "awards"
)

const maxRateBps = 5_000 // 50% a year; anything above is treated as an input error

// Marketplace runs private sealed-bid financing. A bid's rate is sent in the
// transient map (DRUNIX KeyDB transient store), kept in the bidder's implicit
// private data collection, and only its hash goes on the shared ledger.
type Marketplace struct {
	contractapi.Contract
}

// ListReceivable opens an accepted, risk-scored receivable for bids.
func (c *Marketplace) ListReceivable(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	r, err := supplierOwned(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkRiskLimit(ctx, r); err != nil {
		return nil, err
	}
	if err := r.moveTo(StatusListed); err != nil {
		return nil, err
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, "ReceivableListed", map[string]any{
		"id": id, "amountPaise": r.AmountPaise, "dueDate": r.DueDate, "riskScore": r.Risk.Score,
	})
}

// checkRiskLimit refuses receivables whose latest AI score is over the limit.
// It runs at listing and again at award, because the risk service re-scores
// open invoices as new ones arrive (a trading ring only shows once it closes).
func checkRiskLimit(ctx contractapi.TransactionContextInterface, r *Receivable) error {
	if r.Risk == nil {
		return fmt.Errorf("receivable %s has no AI risk score yet", r.ID)
	}
	cfg, err := getRiskConfig(ctx)
	if err != nil {
		return err
	}
	limit := cfg.MaxScoreManual
	if r.EInvoice {
		limit = cfg.MaxScoreEInvoice
	}
	if r.Risk.Score > limit {
		return fmt.Errorf("risk score %d exceeds listing limit %d (reasons %v)", r.Risk.Score, limit, r.Risk.ReasonCodes)
	}
	return nil
}

// BidHash is the commitment that binds a sealed bid to a receivable.
func BidHash(receivableID string, b SealedBid) string {
	return sha256Hex(fmt.Sprintf("%s|%s|%d|%d|%s", receivableID, b.BidID, b.Units, b.RateBps, b.Salt))
}

func implicitCollection(msp string) string { return "_implicit_org_" + msp }

// SubmitBid places a sealed bid. Transient "bid": {"units":..,"rateBps":..,"salt":".."}.
// The bid ID is the transaction ID.
func (c *Marketplace) SubmitBid(ctx contractapi.TransactionContextInterface, id string) (*BidCommitment, error) {
	caller, err := requireRole(ctx, RoleFinancier)
	if err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := r.requireStatus(StatusListed); err != nil {
		return nil, err
	}
	tmap, err := ctx.GetStub().GetTransient()
	if err != nil {
		return nil, err
	}
	raw, ok := tmap[transientBid]
	if !ok {
		return nil, fmt.Errorf("bid must be passed in the transient map under %q", transientBid)
	}
	var bid SealedBid
	if err := json.Unmarshal(raw, &bid); err != nil {
		return nil, fmt.Errorf("bad bid JSON: %w", err)
	}
	if bid.Units <= 0 || bid.Units > TotalUnits {
		return nil, fmt.Errorf("units must be 1-%d", TotalUnits)
	}
	if bid.RateBps <= 0 || bid.RateBps > maxRateBps {
		return nil, fmt.Errorf("rateBps must be 1-%d", maxRateBps)
	}
	if len(bid.Salt) < 16 {
		return nil, fmt.Errorf("salt must be at least 16 characters")
	}
	bid.BidID = ctx.GetStub().GetTxID()

	key, err := compositeKey(ctx, keyBid, id, bid.BidID)
	if err != nil {
		return nil, err
	}
	private, _ := json.Marshal(bid)
	if err := ctx.GetStub().PutPrivateData(implicitCollection(caller.MSP), key, private); err != nil {
		return nil, err
	}
	now, _ := txTime(ctx)
	commit := &BidCommitment{
		DocType: keyBid, BidID: bid.BidID, ReceivableID: id, BidderMSP: caller.MSP,
		BidderID: caller.PartyID, Hash: BidHash(id, bid), Status: "SEALED", CreatedAt: now.Format(time.RFC3339),
	}
	if err := putJSON(ctx, key, commit); err != nil {
		return nil, err
	}
	return commit, emit(ctx, "BidSubmitted", map[string]any{"id": id, "bidId": bid.BidID, "bidderMsp": caller.MSP})
}

// WithdrawBid withdraws the caller's sealed bid while the receivable is listed.
func (c *Marketplace) WithdrawBid(ctx contractapi.TransactionContextInterface, id, bidID string) error {
	caller, err := requireRole(ctx, RoleFinancier)
	if err != nil {
		return err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return err
	}
	if err := r.requireStatus(StatusListed); err != nil {
		return err
	}
	commit, key, err := getBid(ctx, id, bidID)
	if err != nil {
		return err
	}
	if commit.BidderMSP != caller.MSP || commit.Status != "SEALED" {
		return fmt.Errorf("bid %s cannot be withdrawn by %s", bidID, caller.MSP)
	}
	commit.Status = "WITHDRAWN"
	return putJSON(ctx, key, commit)
}

func getBid(ctx contractapi.TransactionContextInterface, id, bidID string) (*BidCommitment, string, error) {
	key, err := compositeKey(ctx, keyBid, id, bidID)
	if err != nil {
		return nil, "", err
	}
	var b BidCommitment
	found, err := getJSON(ctx, key, &b)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", fmt.Errorf("bid %s not found on receivable %s", bidID, id)
	}
	return &b, key, nil
}

// GetMyBid reads the caller's own sealed bid from its private collection.
func (c *Marketplace) GetMyBid(ctx contractapi.TransactionContextInterface, id, bidID string) (*SealedBid, error) {
	caller, err := requireRole(ctx, RoleFinancier)
	if err != nil {
		return nil, err
	}
	key, err := compositeKey(ctx, keyBid, id, bidID)
	if err != nil {
		return nil, err
	}
	b, err := ctx.GetStub().GetPrivateData(implicitCollection(caller.MSP), key)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, fmt.Errorf("bid %s not found in %s", bidID, implicitCollection(caller.MSP))
	}
	var bid SealedBid
	return &bid, json.Unmarshal(b, &bid)
}

// ListBids returns the public commitments (no rates) for a receivable.
func (c *Marketplace) ListBids(ctx contractapi.TransactionContextInterface, id string) ([]*BidCommitment, error) {
	it, err := ctx.GetStub().GetStateByPartialCompositeKey(keyBid, []string{id})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	out := []*BidCommitment{}
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		var b BidCommitment
		if err := json.Unmarshal(kv.Value, &b); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, nil
}

// AwardBids accepts one or more bids. Bidders reveal their bids to the
// supplier's platform off-chain; the supplier passes them in transient
// "awards": [{"bidId","units","rateBps","salt"}]. Each must match its on-chain
// hash, so nobody can alter a bid after the fact. Several awards co-finance
// one invoice; unawarded units stay with the supplier.
func (c *Marketplace) AwardBids(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	r, err := supplierOwned(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := r.requireStatus(StatusListed); err != nil {
		return nil, err
	}
	if err := checkRiskLimit(ctx, r); err != nil {
		return nil, err
	}
	tmap, err := ctx.GetStub().GetTransient()
	if err != nil {
		return nil, err
	}
	raw, ok := tmap[transientAwards]
	if !ok {
		return nil, fmt.Errorf("awards must be passed in the transient map under %q", transientAwards)
	}
	var reveals []SealedBid
	if err := json.Unmarshal(raw, &reveals); err != nil {
		return nil, fmt.Errorf("bad awards JSON: %w", err)
	}
	if len(reveals) == 0 {
		return nil, fmt.Errorf("at least one award is required")
	}
	today, err := txDate(ctx)
	if err != nil {
		return nil, err
	}
	due, _ := parseDate(r.DueDate)
	days := daysBetween(today, due)
	if days < 1 {
		return nil, fmt.Errorf("receivable is due; it can no longer be financed")
	}

	seen := map[string]bool{}
	var unitsTotal int64
	for _, rev := range reveals {
		if seen[rev.BidID] {
			return nil, fmt.Errorf("bid %s awarded twice", rev.BidID)
		}
		seen[rev.BidID] = true
		commit, key, err := getBid(ctx, id, rev.BidID)
		if err != nil {
			return nil, err
		}
		if commit.Status != "SEALED" {
			return nil, fmt.Errorf("bid %s is %s", rev.BidID, commit.Status)
		}
		if BidHash(id, rev) != commit.Hash {
			return nil, fmt.Errorf("revealed bid %s does not match its sealed commitment", rev.BidID)
		}
		unitsTotal += rev.Units
		if unitsTotal > TotalUnits {
			return nil, fmt.Errorf("awards exceed %d units", TotalUnits)
		}
		commit.Status = "AWARDED"
		if err := putJSON(ctx, key, commit); err != nil {
			return nil, err
		}
		r.Awards = append(r.Awards, Award{
			BidID: rev.BidID, FinancierID: commit.BidderID, FinancierMSP: commit.BidderMSP,
			Units: rev.Units, FundingPaise: FundingAmount(r.AmountPaise, rev.Units, rev.RateBps, days),
		})
	}
	if err := markLosingBids(ctx, id, seen); err != nil {
		return nil, err
	}
	if err := r.moveTo(StatusFundingPending); err != nil {
		return nil, err
	}
	now, _ := txTime(ctx)
	r.AwardedAt = now.Format(time.RFC3339)
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	// Off-chain bank adapters listen for this and push IMPS/UPI/NEFT/RTGS payouts to the MSME.
	return r, emit(ctx, "FundingRequested", map[string]any{"id": id, "awards": r.Awards})
}

// markLosingBids marks every other sealed bid LOST. Fabric reads do not see
// this transaction's own writes, so the bids just awarded still read SEALED
// here and are skipped by ID.
func markLosingBids(ctx contractapi.TransactionContextInterface, id string, awarded map[string]bool) error {
	it, err := ctx.GetStub().GetStateByPartialCompositeKey(keyBid, []string{id})
	if err != nil {
		return err
	}
	defer it.Close()
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return err
		}
		var b BidCommitment
		if err := json.Unmarshal(kv.Value, &b); err != nil {
			return err
		}
		if b.Status == "SEALED" && !awarded[b.BidID] {
			b.Status = "LOST"
			if err := putJSON(ctx, kv.Key, &b); err != nil {
				return err
			}
		}
	}
	return nil
}

// FundingAmount is the discounted price a financier pays for its units:
// face share × (1 − rate × days / 365). Computed with big integers so large
// invoices cannot overflow; rounds down to the paisa.
func FundingAmount(amountPaise, units, rateBps, days int64) int64 {
	share := new(big.Int).Mul(big.NewInt(amountPaise), big.NewInt(units))
	share.Quo(share, big.NewInt(TotalUnits))
	discount := new(big.Int).Mul(share, big.NewInt(rateBps))
	discount.Mul(discount, big.NewInt(days))
	discount.Quo(discount, big.NewInt(365*10_000))
	return new(big.Int).Sub(share, discount).Int64()
}
