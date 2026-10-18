// Package contracts holds the RASID chaincode: four contracts that share one
// receivable record on the DRUNIX ledger.
//
//	InvoiceRegistry  – e-invoice verification, uniqueness, buyer acceptance, risk anchoring
//	Marketplace      – listing, sealed bids (commit/reveal via private data), award
//	ReceivableToken  – fractional units and transfers between financiers
//	Settlement       – funding/repayment UTR confirmation, pro-rata payouts, overdue/default
//
// All money is integer paise. All time comes from the transaction timestamp so
// every endorsing peer computes the same result.
package contracts

// TotalUnits is the number of fractional units every receivable is split into.
// One unit is 1 basis point (0.01%) of the invoice face value.
const TotalUnits int64 = 10_000

// Receivable is the on-ledger token for one invoice. Only hashes and the
// minimum metadata needed for financing are stored on the shared ledger.
type Receivable struct {
	DocType    string `json:"docType"`
	ID         string `json:"id"`
	IRNHash    string `json:"irnHash,omitempty" metadata:"irnHash,optional"`
	EInvoice   bool   `json:"eInvoice"`
	SupplierID string `json:"supplierId"`
	BuyerID    string `json:"buyerId"`
	OriginMSP  string `json:"originMsp"`

	AmountPaise int64  `json:"amountPaise"`
	InvoiceDate string `json:"invoiceDate"` // YYYY-MM-DD
	DueDate     string `json:"dueDate"`     // YYYY-MM-DD
	Status      Status `json:"status"`

	Risk *RiskScore `json:"risk,omitempty" metadata:"risk,optional"`

	Awards       []Award  `json:"awards,omitempty" metadata:"awards,optional"`
	AwardedAt    string   `json:"awardedAt,omitempty" metadata:"awardedAt,optional"`
	MandateRef   string   `json:"mandateRef,omitempty" metadata:"mandateRef,optional"`
	MandateRail  string   `json:"mandateRail,omitempty" metadata:"mandateRail,optional"`
	FundedPaise  int64    `json:"fundedPaise"`
	RepaidPaise  int64    `json:"repaidPaise"`
	Payouts      []Payout `json:"payouts,omitempty" metadata:"payouts,optional"`
	DaysLate     int64    `json:"daysLate,omitempty" metadata:"daysLate,optional"`
	Breach45Days bool     `json:"breach45Days"`

	RejectReason string `json:"rejectReason,omitempty" metadata:"rejectReason,optional"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// RiskScore is the signed output of the off-chain AI fraud/risk service.
// The full explanation stays off-chain; its hash anchors it here.
type RiskScore struct {
	Score        int      `json:"score"` // 0 (clean) – 100 (certain fraud)
	ModelVersion string   `json:"modelVersion"`
	ReasonCodes  []string `json:"reasonCodes,omitempty" metadata:"reasonCodes,optional"`
	ReportHash   string   `json:"reportHash"`
	ScoredBy     string   `json:"scoredBy"`
	ScoredAt     string   `json:"scoredAt"`
}

// BidCommitment is the public trace of a sealed bid. The rate never appears
// on the shared ledger, only the hash of the full bid.
type BidCommitment struct {
	DocType      string `json:"docType"`
	BidID        string `json:"bidId"`
	ReceivableID string `json:"receivableId"`
	BidderMSP    string `json:"bidderMsp"`
	BidderID     string `json:"bidderId"`
	Hash         string `json:"hash"`
	Status       string `json:"status"` // SEALED, WITHDRAWN, AWARDED, LOST
	CreatedAt    string `json:"createdAt"`
}

// SealedBid is the private bid. It travels only in the transient map and the
// bidder's implicit private data collection.
type SealedBid struct {
	BidID   string `json:"bidId"`
	Units   int64  `json:"units"`
	RateBps int64  `json:"rateBps"` // annual discount rate in basis points
	Salt    string `json:"salt"`
}

// Award records a winning bid and its funding leg.
type Award struct {
	BidID        string `json:"bidId"`
	FinancierID  string `json:"financierId"`
	FinancierMSP string `json:"financierMsp"`
	Units        int64  `json:"units"`
	FundingPaise int64  `json:"fundingPaise"`
	FundingUTR   string `json:"fundingUtr,omitempty" metadata:"fundingUtr,optional"`
	Rail         string `json:"rail,omitempty" metadata:"rail,optional"`
	Confirmed    bool   `json:"confirmed"`
}

// Payout is one holder's share of a buyer repayment.
type Payout struct {
	PayoutID     string `json:"payoutId"`
	HolderID     string `json:"holderId"`
	AmountPaise  int64  `json:"amountPaise"`
	RepaymentUTR string `json:"repaymentUtr"`
	PayoutUTR    string `json:"payoutUtr,omitempty" metadata:"payoutUtr,optional"`
	Confirmed    bool   `json:"confirmed"`
}

// Holding is one party's units of a receivable.
type Holding struct {
	ReceivableID string `json:"receivableId"`
	HolderID     string `json:"holderId"`
	Units        int64  `json:"units"`
}
