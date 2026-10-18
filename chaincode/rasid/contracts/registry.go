package contracts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// InvoiceRegistry registers invoices as receivable tokens, blocks duplicates
// network-wide, records buyer acceptance and anchors AI risk scores.
type InvoiceRegistry struct {
	contractapi.Contract
}

// InitLedger runs once, right after the chaincode is committed (deploy with
// --init-required). It records the network operator MSP and the MSPs whose
// risk-oracle identities may anchor AI scores. The caller must be an operator
// identity from operatorMSP itself.
func (c *InvoiceRegistry) InitLedger(ctx contractapi.TransactionContextInterface, operatorMSP string, riskOracleMSPs []string) error {
	key, err := compositeKey(ctx, keyConfig, "network")
	if err != nil {
		return err
	}
	if existing, err := ctx.GetStub().GetState(key); err != nil {
		return err
	} else if existing != nil {
		return fmt.Errorf("network is already initialised")
	}
	caller, err := getCaller(ctx)
	if err != nil {
		return err
	}
	if caller.Role != RoleOperator || caller.MSP != operatorMSP {
		return fmt.Errorf("InitLedger must be called by an operator identity from %s", operatorMSP)
	}
	if len(riskOracleMSPs) == 0 {
		return fmt.Errorf("at least one risk oracle MSP is required")
	}
	return putJSON(ctx, key, NetworkConfig{OperatorMSP: operatorMSP, RiskOracleMSPs: riskOracleMSPs})
}

// SetIRPPublicKey stores a GST IRP signing key (PEM) under its key ID.
// Operator only; the IRP rotates keys, so several kids can be active.
func (c *InvoiceRegistry) SetIRPPublicKey(ctx contractapi.TransactionContextInterface, kid, pemKey string) error {
	if _, err := requireRole(ctx, RoleOperator); err != nil {
		return err
	}
	if _, err := ParseRSAPublicKeyPEM(pemKey); err != nil {
		return fmt.Errorf("invalid IRP key: %w", err)
	}
	key, err := compositeKey(ctx, keyIRPKey, kid)
	if err != nil {
		return err
	}
	return ctx.GetStub().PutState(key, []byte(pemKey))
}

// SetRiskThresholds sets the maximum AI risk score allowed for listing.
func (c *InvoiceRegistry) SetRiskThresholds(ctx contractapi.TransactionContextInterface, maxEInvoice, maxManual int) error {
	if _, err := requireRole(ctx, RoleOperator); err != nil {
		return err
	}
	if maxEInvoice < 0 || maxEInvoice > 100 || maxManual < 0 || maxManual > 100 {
		return fmt.Errorf("thresholds must be 0-100")
	}
	key, err := compositeKey(ctx, keyConfig, "risk")
	if err != nil {
		return err
	}
	return putJSON(ctx, key, RiskConfig{MaxScoreEInvoice: maxEInvoice, MaxScoreManual: maxManual})
}

// RegisterEInvoice verifies an IRP signed QR and registers the invoice.
// The caller must be the supplier named in the e-invoice.
func (c *InvoiceRegistry) RegisterEInvoice(ctx contractapi.TransactionContextInterface, signedQR, dueDate string) (*Receivable, error) {
	caller, err := requireRole(ctx, RoleSupplier)
	if err != nil {
		return nil, err
	}
	kid, err := signedQRHeaderKid(signedQR)
	if err != nil {
		return nil, err
	}
	keyKey, err := compositeKey(ctx, keyIRPKey, kid)
	if err != nil {
		return nil, err
	}
	pemKey, err := ctx.GetStub().GetState(keyKey)
	if err != nil {
		return nil, err
	}
	if pemKey == nil {
		return nil, fmt.Errorf("no IRP public key registered for kid %q", kid)
	}
	pub, err := ParseRSAPublicKeyPEM(string(pemKey))
	if err != nil {
		return nil, err
	}
	inv, err := VerifySignedQR(signedQR, pub)
	if err != nil {
		return nil, err
	}
	if PartyIDForGSTIN(inv.SellerGstin) != caller.PartyID {
		return nil, fmt.Errorf("caller is not the seller on this e-invoice")
	}
	if inv.DocTyp != "" && inv.DocTyp != "INV" {
		return nil, fmt.Errorf("only tax invoices (DocTyp INV) can be financed, got %s", inv.DocTyp)
	}
	invoiceDate, err := inv.DocDate()
	if err != nil {
		return nil, err
	}
	irnHash := sha256Hex("IRN:" + strings.ToLower(inv.Irn))
	irnKey, err := compositeKey(ctx, keyIRN, irnHash)
	if err != nil {
		return nil, err
	}
	if existing, err := ctx.GetStub().GetState(irnKey); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, fmt.Errorf("DUPLICATE: IRN already registered as receivable %s", existing)
	}
	r, err := newReceivable(ctx, caller, inv.SellerGstin, inv.BuyerGstin, inv.DocNo, invoiceDate, dueDate, inv.AmountPaise())
	if err != nil {
		return nil, err
	}
	r.EInvoice = true
	r.IRNHash = irnHash
	if err := ctx.GetStub().PutState(irnKey, []byte(r.ID)); err != nil {
		return nil, err
	}
	return r, saveNew(ctx, r)
}

// RegisterManualInvoice registers an invoice outside the e-invoice mandate.
// It needs buyer acceptance (the co-signature) and faces a stricter risk threshold.
func (c *InvoiceRegistry) RegisterManualInvoice(ctx contractapi.TransactionContextInterface,
	sellerGSTIN, buyerGSTIN, docNo, invoiceDate, dueDate string, amountPaise int64) (*Receivable, error) {
	caller, err := requireRole(ctx, RoleSupplier)
	if err != nil {
		return nil, err
	}
	if PartyIDForGSTIN(sellerGSTIN) != caller.PartyID {
		return nil, fmt.Errorf("caller is not the seller GSTIN")
	}
	r, err := newReceivable(ctx, caller, sellerGSTIN, buyerGSTIN, docNo, invoiceDate, dueDate, amountPaise)
	if err != nil {
		return nil, err
	}
	return r, saveNew(ctx, r)
}

func newReceivable(ctx contractapi.TransactionContextInterface, caller Caller,
	sellerGSTIN, buyerGSTIN, docNo, invoiceDate, dueDate string, amountPaise int64) (*Receivable, error) {
	if amountPaise <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	if strings.EqualFold(strings.TrimSpace(sellerGSTIN), strings.TrimSpace(buyerGSTIN)) {
		return nil, fmt.Errorf("seller and buyer GSTIN are the same")
	}
	inv, err := parseDate(invoiceDate)
	if err != nil {
		return nil, err
	}
	due, err := parseDate(dueDate)
	if err != nil {
		return nil, err
	}
	if !due.After(inv) {
		return nil, fmt.Errorf("due date must be after invoice date")
	}
	today, err := txDate(ctx)
	if err != nil {
		return nil, err
	}
	if !due.After(today) {
		return nil, fmt.Errorf("invoice is already due; only future receivables can be registered")
	}
	if inv.After(today) {
		return nil, fmt.Errorf("invoice date %s is in the future", invoiceDate)
	}
	fy, err := FinancialYear(invoiceDate)
	if err != nil {
		return nil, err
	}
	now, _ := txTime(ctx)
	return &Receivable{
		ID:          ReceivableIDFor(sellerGSTIN, docNo, fy),
		SupplierID:  caller.PartyID,
		BuyerID:     PartyIDForGSTIN(buyerGSTIN),
		OriginMSP:   caller.MSP,
		AmountPaise: amountPaise,
		InvoiceDate: invoiceDate,
		DueDate:     dueDate,
		Status:      StatusRegistered,
		CreatedAt:   now.Format(time.RFC3339),
	}, nil
}

// saveNew writes a new receivable, refusing duplicates. Two concurrent
// registrations of the same invoice both read the key as empty, but Fabric's
// MVCC check invalidates the second one at commit.
func saveNew(ctx contractapi.TransactionContextInterface, r *Receivable) error {
	key, err := compositeKey(ctx, keyReceivable, r.ID)
	if err != nil {
		return err
	}
	existing, err := ctx.GetStub().GetState(key)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("DUPLICATE: invoice already registered as receivable %s", r.ID)
	}
	if err := putReceivable(ctx, r); err != nil {
		return err
	}
	if err := setHolding(ctx, r.ID, r.SupplierID, TotalUnits); err != nil {
		return err
	}
	return emit(ctx, "ReceivableRegistered", map[string]any{
		"id": r.ID, "eInvoice": r.EInvoice, "originMsp": r.OriginMSP, "amountPaise": r.AmountPaise,
	})
}

// AcceptInvoice is the buyer's irrevocable acknowledgement of the obligation.
func (c *InvoiceRegistry) AcceptInvoice(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	r, err := buyerAction(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := r.moveTo(StatusAccepted); err != nil {
		return nil, err
	}
	if err := putReceivable(ctx, r); err != nil {
		return nil, err
	}
	return r, emit(ctx, "ReceivableAccepted", map[string]any{"id": id})
}

// RejectInvoice lets the buyer dispute an invoice it does not recognise.
func (c *InvoiceRegistry) RejectInvoice(ctx contractapi.TransactionContextInterface, id, reasonCode string) error {
	r, err := buyerAction(ctx, id)
	if err != nil {
		return err
	}
	if err := r.moveTo(StatusRejected); err != nil {
		return err
	}
	r.RejectReason = reasonCode
	if err := putReceivable(ctx, r); err != nil {
		return err
	}
	return emit(ctx, "ReceivableRejected", map[string]any{"id": id, "reason": reasonCode})
}

func buyerAction(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	caller, err := requireRole(ctx, RoleBuyer)
	if err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.BuyerID != caller.PartyID {
		return nil, fmt.Errorf("caller is not the buyer on receivable %s", id)
	}
	return r, nil
}

// CancelInvoice withdraws an invoice before it is financed. The uniqueness
// key stays, so a cancelled invoice cannot be re-registered and financed elsewhere.
func (c *InvoiceRegistry) CancelInvoice(ctx contractapi.TransactionContextInterface, id string) error {
	r, err := supplierOwned(ctx, id)
	if err != nil {
		return err
	}
	if err := r.moveTo(StatusCancelled); err != nil {
		return err
	}
	if err := putReceivable(ctx, r); err != nil {
		return err
	}
	return emit(ctx, "ReceivableCancelled", map[string]any{"id": id})
}

func supplierOwned(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	caller, err := requireRole(ctx, RoleSupplier)
	if err != nil {
		return nil, err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.SupplierID != caller.PartyID {
		return nil, fmt.Errorf("caller is not the supplier on receivable %s", id)
	}
	return r, nil
}

// RecordRiskScore anchors the AI service's signed score on the token. The
// detailed report stays off-chain; reportHash proves which report was used.
func (c *InvoiceRegistry) RecordRiskScore(ctx contractapi.TransactionContextInterface,
	id string, score int, modelVersion string, reasonCodes []string, reportHash string) error {
	caller, err := requireRole(ctx, RoleRiskOracle)
	if err != nil {
		return err
	}
	if score < 0 || score > 100 {
		return fmt.Errorf("score must be 0-100")
	}
	if modelVersion == "" || reportHash == "" {
		return fmt.Errorf("modelVersion and reportHash are required")
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return err
	}
	// LISTED is allowed so a re-score can still block the award.
	if err := r.requireStatus(StatusRegistered, StatusAccepted, StatusListed); err != nil {
		return err
	}
	now, _ := txTime(ctx)
	r.Risk = &RiskScore{
		Score: score, ModelVersion: modelVersion, ReasonCodes: reasonCodes,
		ReportHash: reportHash, ScoredBy: caller.MSP, ScoredAt: now.Format(time.RFC3339),
	}
	if err := putReceivable(ctx, r); err != nil {
		return err
	}
	return emit(ctx, "RiskScored", map[string]any{"id": id, "score": score, "modelVersion": modelVersion})
}

// GetReceivable returns one receivable.
func (c *InvoiceRegistry) GetReceivable(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	return getReceivable(ctx, id)
}

// CheckDuplicate lets any member test an invoice before financing it off-network.
func (c *InvoiceRegistry) CheckDuplicate(ctx contractapi.TransactionContextInterface, sellerGSTIN, docNo, invoiceDate string) (bool, error) {
	fy, err := FinancialYear(invoiceDate)
	if err != nil {
		return false, err
	}
	key, err := compositeKey(ctx, keyReceivable, ReceivableIDFor(sellerGSTIN, docNo, fy))
	if err != nil {
		return false, err
	}
	b, err := ctx.GetStub().GetState(key)
	return b != nil, err
}

// GetReceivableHistory returns every committed version of a receivable.
func (c *InvoiceRegistry) GetReceivableHistory(ctx contractapi.TransactionContextInterface, id string) ([]string, error) {
	key, err := compositeKey(ctx, keyReceivable, id)
	if err != nil {
		return nil, err
	}
	it, err := ctx.GetStub().GetHistoryForKey(key)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	out := []string{}
	for it.HasNext() {
		m, err := it.Next()
		if err != nil {
			return nil, err
		}
		out = append(out, string(m.Value))
	}
	return out, nil
}

// ReceivablePage is one page of ListReceivables.
type ReceivablePage struct {
	Records  []*Receivable `json:"records"`
	Bookmark string        `json:"bookmark" metadata:"bookmark,optional"`
}

const maxPageSize = 200

// ListReceivables pages through receivables in key order, optionally keeping
// only one status (so a page can hold fewer than pageSize records). Pass the
// returned bookmark to get the next page; an empty bookmark means the end.
// Read-only: Fabric allows paginated queries only outside submitted transactions.
func (c *InvoiceRegistry) ListReceivables(ctx contractapi.TransactionContextInterface, status string, pageSize int, bookmark string) (*ReceivablePage, error) {
	if pageSize <= 0 || pageSize > maxPageSize {
		return nil, fmt.Errorf("pageSize must be 1-%d", maxPageSize)
	}
	it, meta, err := ctx.GetStub().GetStateByPartialCompositeKeyWithPagination(keyReceivable, []string{}, int32(pageSize), bookmark)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	page := &ReceivablePage{Records: []*Receivable{}}
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		var r Receivable
		if err := json.Unmarshal(kv.Value, &r); err != nil {
			return nil, err
		}
		if status == "" || string(r.Status) == status {
			page.Records = append(page.Records, &r)
		}
	}
	if meta != nil && meta.FetchedRecordsCount == int32(pageSize) {
		page.Bookmark = meta.Bookmark
	}
	return page, nil
}
