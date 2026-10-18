package contracts

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

const (
	sellerGSTIN = "27AAACM1234A1Z5"
	buyerGSTIN  = "29AABCB5678B1Z2"
	originMSP   = "SupplierBankMSP"
	buyerMSP    = "BuyerBankMSP"
	citiMSP     = "CitiMSP"
	nbfcMSP     = "NbfcMSP"
	thirdMSP    = "OtherBankMSP"
	npciMSP     = "NPCIMSP"
	riskMSP     = "RiskMSP"
)

var start = time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)

var (
	reg = &InvoiceRegistry{}
	mkt = &Marketplace{}
	tok = &ReceivableToken{}
	stl = &Settlement{}
)

// mockIRP signs e-invoice QR codes the way the GST IRP does (RS256 JWT).
type mockIRP struct{ key *rsa.PrivateKey }

func newMockIRP(t *testing.T) *mockIRP {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &mockIRP{key: k}
}

func (m *mockIRP) publicPEM() string {
	der, _ := x509.MarshalPKIXPublicKey(&m.key.PublicKey)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func (m *mockIRP) sign(t *testing.T, d EInvoiceData) string {
	data, _ := json.Marshal(d)
	payload, _ := json.Marshal(map[string]string{"data": string(data), "iss": "NIC"})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","kid":"irp-2026","typ":"JWT"}`)) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + enc.EncodeToString(sig)
}

func sampleInvoice() EInvoiceData {
	return EInvoiceData{
		SellerGstin: sellerGSTIN, BuyerGstin: buyerGSTIN, DocNo: "MSME/26-27/0042", DocTyp: "INV",
		DocDt: "10/11/2026", TotInvVal: 1000000.00, ItemCnt: 3, MainHsnCode: "8481",
		Irn: "a5c12dca80e743321740b001fd70953e8738d109865d28ba4013750f2046f229", IrnDt: "2026-11-10 12:00:00",
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustFail(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %v", want, err)
	}
}

// setup registers, accepts and scores one e-invoice.
func setup(t *testing.T) (*world, *mockIRP, string) {
	w := newWorld(start)
	irp := newMockIRP(t)
	must(t, reg.InitLedger(w.as(role(npciMSP, RoleOperator)), npciMSP, []string{riskMSP}))
	must(t, reg.SetIRPPublicKey(w.as(role(npciMSP, RoleOperator)), "irp-2026", irp.publicPEM()))
	r, err := reg.RegisterEInvoice(w.as(supplier(originMSP, sellerGSTIN)), irp.sign(t, sampleInvoice()), "2027-01-09")
	must(t, err)
	if r.AmountPaise != 100000000 || !r.EInvoice || r.InvoiceDate != "2026-11-10" {
		t.Fatalf("unexpected receivable %+v", r)
	}
	_, err = reg.AcceptInvoice(w.as(buyer(buyerMSP, buyerGSTIN)), r.ID)
	must(t, err)
	must(t, reg.RecordRiskScore(w.as(role(riskMSP, RoleRiskOracle)), r.ID, 18, "rasid-gnn-0.1", []string{"OK"}, sha256Hex("report")))
	return w, irp, r.ID
}

func bid(t *testing.T, w *world, id, msp string, units, rate int64, salt string) SealedBid {
	b, _ := json.Marshal(SealedBid{Units: units, RateBps: rate, Salt: salt})
	c, err := mkt.SubmitBid(w.withTransient(w.as(role(msp, RoleFinancier)), transientBid, b), id)
	must(t, err)
	return SealedBid{BidID: c.BidID, Units: units, RateBps: rate, Salt: salt}
}

func TestFullLifecycle(t *testing.T) {
	w, irp, id := setup(t)
	sup := supplier(originMSP, sellerGSTIN)

	// The same invoice cannot come back, through either route.
	_, err := reg.RegisterEInvoice(w.as(sup), irp.sign(t, sampleInvoice()), "2027-01-09")
	mustFail(t, err, "DUPLICATE")
	_, err = reg.RegisterManualInvoice(w.as(sup), sellerGSTIN, buyerGSTIN, "msme/26-27/0042", "2026-11-10", "2027-01-09", 100000000)
	mustFail(t, err, "DUPLICATE")
	all, err := reg.ListReceivables(w.as(sup), "", 50, "")
	must(t, err)
	listed, _ := reg.ListReceivables(w.as(sup), "LISTED", 50, "")
	if len(all.Records) != 1 || len(listed.Records) != 0 || all.Bookmark != "" {
		t.Fatalf("ListReceivables = %+v / %+v", all, listed)
	}
	dup, err := reg.CheckDuplicate(w.as(role(citiMSP, RoleFinancier)), sellerGSTIN, "MSME/26-27/0042", "2026-11-10")
	must(t, err)
	if !dup {
		t.Fatal("CheckDuplicate should report the registered invoice")
	}

	_, err = mkt.ListReceivable(w.as(sup), id)
	must(t, err)

	citi := bid(t, w, id, citiMSP, 6000, 1000, "citi-salt-0123456789")
	nbfc := bid(t, w, id, nbfcMSP, 4000, 1200, "nbfc-salt-0123456789")
	bid(t, w, id, thirdMSP, 10000, 1500, "other-salt-012345678")

	// Rates never reach the public ledger.
	w.stub.commit()
	for k, v := range w.stub.state {
		if strings.Contains(k, keyBid) && strings.Contains(string(v), "rateBps") {
			t.Fatalf("rate leaked into public state at %q", k)
		}
	}

	awards, _ := json.Marshal([]SealedBid{citi, nbfc})
	r, err := mkt.AwardBids(w.withTransient(w.as(sup), transientAwards, awards), id)
	must(t, err)
	if r.Status != StatusFundingPending || len(r.Awards) != 2 {
		t.Fatalf("award failed: %+v", r)
	}
	// 55 days to maturity: ₹6,00,000 × (1 − 10% × 55/365) = ₹5,90,958.91 (discount rounds down)
	if r.Awards[0].FundingPaise != 59095891 {
		t.Fatalf("Citi funding = %d", r.Awards[0].FundingPaise)
	}
	bids, _ := mkt.ListBids(w.as(sup), id)
	lost := 0
	for _, b := range bids {
		if b.Status == "LOST" {
			lost++
		}
	}
	if lost != 1 {
		t.Fatalf("expected one losing bid, got %d", lost)
	}

	_, err = stl.ConfirmFunding(w.as(role(citiMSP, RoleFinancier)), id, citi.BidID, "IMPS000111", "IMPS", 1)
	mustFail(t, err, "award requires")
	_, err = stl.ConfirmFunding(w.as(role(citiMSP, RoleFinancier)), id, citi.BidID, "IMPS000111", "IMPS", r.Awards[0].FundingPaise)
	must(t, err)
	_, err = stl.ConfirmFunding(w.as(role(nbfcMSP, RoleFinancier)), id, nbfc.BidID, "IMPS000111", "IMPS", r.Awards[1].FundingPaise)
	mustFail(t, err, "already used")
	r, err = stl.ConfirmFunding(w.as(role(nbfcMSP, RoleFinancier)), id, nbfc.BidID, "NEFT000222", "NEFT", r.Awards[1].FundingPaise)
	must(t, err)
	if r.Status != StatusFunded {
		t.Fatalf("status = %s", r.Status)
	}

	// Secondary sale: the NBFC sells 1,000 units to Citi.
	must(t, tok.TransferUnits(w.as(role(nbfcMSP, RoleFinancier)), id, citiMSP, 1000))
	h, _ := tok.GetHoldings(w.as(sup), id)
	units := map[string]int64{}
	for _, x := range h {
		units[x.HolderID] = x.Units
	}
	if units[FinancierID(citiMSP)] != 7000 || units[FinancierID(nbfcMSP)] != 3000 || len(units) != 2 {
		t.Fatalf("holdings = %v", units)
	}

	must(t, stl.RegisterMandate(w.as(buyer(buyerMSP, buyerGSTIN)), id, "NACH-UMRN-0001", "E_NACH"))
	w.stub.now = time.Date(2027, 1, 9, 9, 0, 0, 0, time.UTC)
	r, err = stl.ConfirmRepayment(w.as(buyer(buyerMSP, buyerGSTIN)), id, "NACH000333", 100000000)
	must(t, err)
	if r.Status != StatusRepaid || len(r.Payouts) != 2 || !r.Breach45Days {
		t.Fatalf("repayment: %+v", r)
	}
	paid := map[string]int64{}
	for _, p := range r.Payouts {
		paid[p.HolderID] = p.AmountPaise
	}
	if paid[FinancierID(citiMSP)] != 70000000 || paid[FinancierID(nbfcMSP)] != 30000000 {
		t.Fatalf("payouts = %v", paid)
	}
	op := role(npciMSP, RoleOperator)
	r, err = stl.ConfirmPayout(w.as(op), id, r.Payouts[0].PayoutID, "RTGS000444")
	must(t, err)
	if r.Status != StatusRepaid {
		t.Fatal("closed too early")
	}
	r, err = stl.ConfirmPayout(w.as(op), id, r.Payouts[1].PayoutID, "RTGS000555")
	must(t, err)
	if r.Status != StatusClosed {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestForgedQRIsRejected(t *testing.T) {
	w, irp, _ := setup(t)
	inv := sampleInvoice()
	inv.DocNo = "MSME/26-27/0043"
	parts := strings.Split(irp.sign(t, inv), ".")
	inv.TotInvVal = 9900000 // inflate the amount after signing
	data, _ := json.Marshal(inv)
	payload, _ := json.Marshal(map[string]string{"data": string(data)})
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]
	_, err := reg.RegisterEInvoice(w.as(supplier(originMSP, sellerGSTIN)), forged, "2027-01-09")
	mustFail(t, err, "signature invalid")

	other := newMockIRP(t) // signed by a key the network does not trust
	_, err = reg.RegisterEInvoice(w.as(supplier(originMSP, sellerGSTIN)), other.sign(t, inv), "2027-01-09")
	mustFail(t, err, "signature invalid")
}

func TestOnlyTheSellerCanRegister(t *testing.T) {
	w, irp, _ := setup(t)
	inv := sampleInvoice()
	inv.DocNo = "MSME/26-27/0050"
	_, err := reg.RegisterEInvoice(w.as(supplier(originMSP, "07AAAAA0000A1Z9")), irp.sign(t, inv), "2027-01-09")
	mustFail(t, err, "not the seller")
}

func TestRolesAreEnforced(t *testing.T) {
	w, _, _ := setup(t)
	r, err := reg.RegisterManualInvoice(w.as(supplier(originMSP, sellerGSTIN)), sellerGSTIN, buyerGSTIN, "M-1", "2026-11-12", "2027-02-10", 5000000)
	must(t, err)
	_, err = reg.AcceptInvoice(w.as(role(citiMSP, RoleFinancier)), r.ID)
	mustFail(t, err, "may not call")
	_, err = reg.AcceptInvoice(w.as(buyer(buyerMSP, "33AAACX9999X1Z1")), r.ID)
	mustFail(t, err, "not the buyer")
	mustFail(t, reg.RecordRiskScore(w.as(supplier(originMSP, sellerGSTIN)), r.ID, 1, "m", nil, "h"), "may not call")
	_, err = reg.RegisterManualInvoice(w.as(supplier(originMSP, sellerGSTIN)), sellerGSTIN, buyerGSTIN, "M-9", "2026-12-01", "2027-02-10", 5000000)
	mustFail(t, err, "in the future")
}

func TestPrivilegedRolesAreBoundToTheirMSP(t *testing.T) {
	w, irp, id := setup(t)
	// A member bank's CA mints an "operator" or "risk_oracle" certificate for itself.
	mustFail(t, reg.SetIRPPublicKey(w.as(role(citiMSP, RoleOperator)), "evil", irp.publicPEM()), "not an authorised operator")
	mustFail(t, reg.RecordRiskScore(w.as(role(citiMSP, RoleRiskOracle)), id, 0, "m", nil, "h"), "not an authorised risk_oracle")
	mustFail(t, reg.InitLedger(w.as(role(citiMSP, RoleOperator)), citiMSP, []string{citiMSP}), "already initialised")
}

func TestUnfundedAwardsLapse(t *testing.T) {
	w, _, id := setup(t)
	sup := supplier(originMSP, sellerGSTIN)
	_, err := mkt.ListReceivable(w.as(sup), id)
	must(t, err)
	citi := bid(t, w, id, citiMSP, 6000, 1000, "citi-salt-0123456789")
	nbfc := bid(t, w, id, nbfcMSP, 4000, 1200, "nbfc-salt-0123456789")
	awards, _ := json.Marshal([]SealedBid{citi, nbfc})
	r, err := mkt.AwardBids(w.withTransient(w.as(sup), transientAwards, awards), id)
	must(t, err)
	_, err = stl.ConfirmFunding(w.as(role(citiMSP, RoleFinancier)), id, citi.BidID, "IMPS0001", "IMPS", r.Awards[0].FundingPaise)
	must(t, err)

	_, err = stl.ExpireUnfundedAwards(w.as(sup), id)
	mustFail(t, err, "have until")
	w.stub.now = w.stub.now.Add(25 * time.Hour)
	r, err = stl.ExpireUnfundedAwards(w.as(sup), id)
	must(t, err)
	if r.Status != StatusFunded || len(r.Awards) != 1 {
		t.Fatalf("after lapse: %+v", r)
	}
	h, _ := tok.GetHoldings(w.as(sup), id)
	if len(h) != 2 { // Citi 6,000 units, supplier keeps 4,000
		t.Fatalf("holdings = %+v", h)
	}
}

func TestManualInvoiceHasStricterRiskLimit(t *testing.T) {
	w, _, _ := setup(t)
	sup := supplier(originMSP, sellerGSTIN)
	r, err := reg.RegisterManualInvoice(w.as(sup), sellerGSTIN, buyerGSTIN, "M-2", "2026-11-12", "2027-02-10", 5000000)
	must(t, err)
	_, err = mkt.ListReceivable(w.as(sup), r.ID)
	mustFail(t, err, "no AI risk score")
	must(t, reg.RecordRiskScore(w.as(role(riskMSP, RoleRiskOracle)), r.ID, 60, "rasid-gnn-0.1", []string{"CIRCULAR_TRADE"}, "h"))
	_, err = reg.AcceptInvoice(w.as(buyer(buyerMSP, buyerGSTIN)), r.ID)
	must(t, err)
	_, err = mkt.ListReceivable(w.as(sup), r.ID)
	mustFail(t, err, "exceeds listing limit 50")
}

func TestRevealMustMatchSealedBid(t *testing.T) {
	w, _, id := setup(t)
	sup := supplier(originMSP, sellerGSTIN)
	_, err := mkt.ListReceivable(w.as(sup), id)
	must(t, err)
	b := bid(t, w, id, citiMSP, 10000, 1100, "citi-salt-0123456789")

	own, err := mkt.GetMyBid(w.as(role(citiMSP, RoleFinancier)), id, b.BidID)
	must(t, err)
	if own.RateBps != 1100 {
		t.Fatalf("private bid = %+v", own)
	}
	_, err = mkt.GetMyBid(w.as(role(nbfcMSP, RoleFinancier)), id, b.BidID)
	mustFail(t, err, "not found")

	b.RateBps = 900 // supplier tries to award at a better rate than was bid
	awards, _ := json.Marshal([]SealedBid{b})
	_, err = mkt.AwardBids(w.withTransient(w.as(sup), transientAwards, awards), id)
	mustFail(t, err, "does not match")
}

func TestRescoreBlocksAward(t *testing.T) {
	w, _, id := setup(t)
	sup := supplier(originMSP, sellerGSTIN)
	_, err := mkt.ListReceivable(w.as(sup), id)
	must(t, err)
	b := bid(t, w, id, citiMSP, 10000, 1100, "citi-salt-0123456789")
	// A later invoice closes a trading ring through this one.
	must(t, reg.RecordRiskScore(w.as(role(riskMSP, RoleRiskOracle)), id, 75, "rasid-graph-0.1", []string{"CIRCULAR_TRADE"}, "h2"))
	awards, _ := json.Marshal([]SealedBid{b})
	_, err = mkt.AwardBids(w.withTransient(w.as(sup), transientAwards, awards), id)
	mustFail(t, err, "exceeds listing limit")
}

func TestOverdueAndDefault(t *testing.T) {
	w, _, id := setup(t)
	sup := supplier(originMSP, sellerGSTIN)
	_, err := mkt.ListReceivable(w.as(sup), id)
	must(t, err)
	b := bid(t, w, id, citiMSP, 10000, 1100, "citi-salt-0123456789")
	awards, _ := json.Marshal([]SealedBid{b})
	r, err := mkt.AwardBids(w.withTransient(w.as(sup), transientAwards, awards), id)
	must(t, err)
	_, err = stl.ConfirmFunding(w.as(role(citiMSP, RoleFinancier)), id, b.BidID, "UPI0001", "UPI", r.Awards[0].FundingPaise)
	must(t, err)

	anyone := role(citiMSP, RoleFinancier)
	_, err = stl.MarkOverdue(w.as(anyone), id)
	mustFail(t, err, "not past its due date")

	w.stub.now = time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)
	r, err = stl.MarkOverdue(w.as(anyone), id)
	must(t, err)
	if r.Status != StatusOverdue || r.DaysLate != 11 || !r.Breach45Days {
		t.Fatalf("overdue: %+v", r)
	}
	_, err = stl.MarkDefaulted(w.as(role(npciMSP, RoleOperator)), id)
	mustFail(t, err, "not 90 days past due")

	w.stub.now = time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	r, err = stl.MarkDefaulted(w.as(role(npciMSP, RoleOperator)), id)
	must(t, err)
	if r.Status != StatusDefaulted {
		t.Fatalf("status = %s", r.Status)
	}
	// Recovery after default still settles and pays holders.
	r, err = stl.ConfirmRepayment(w.as(role(npciMSP, RoleOperator)), id, "NACH0009", 40000000)
	must(t, err)
	if r.Status != StatusDefaulted || r.RepaidPaise != 40000000 {
		t.Fatalf("partial recovery: %+v", r)
	}
}

func TestListReceivablesPages(t *testing.T) {
	w, _, _ := setup(t)
	sup := supplier(originMSP, sellerGSTIN)
	for i := 0; i < 4; i++ {
		_, err := reg.RegisterManualInvoice(w.as(sup), sellerGSTIN, buyerGSTIN, fmt.Sprintf("P-%d", i), "2026-11-12", "2027-02-10", 5000000)
		must(t, err)
	}
	seen := map[string]bool{}
	bookmark := ""
	for pages := 0; pages < 10; pages++ {
		p, err := reg.ListReceivables(w.as(sup), "", 2, bookmark)
		must(t, err)
		for _, r := range p.Records {
			seen[r.ID] = true
		}
		if bookmark = p.Bookmark; bookmark == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d receivables, want 5", len(seen))
	}
	_, err := reg.ListReceivables(w.as(sup), "", 0, "")
	mustFail(t, err, "pageSize")
}

func TestProRataAddsUp(t *testing.T) {
	h := []Holding{{HolderID: "a", Units: 3333}, {HolderID: "b", Units: 3333}, {HolderID: "c", Units: 3334}}
	s := ProRata(100001, h)
	if s[0]+s[1]+s[2] != 100001 || s[2] != 33341 {
		t.Fatalf("shares = %v", s)
	}
}

func TestFinancialYear(t *testing.T) {
	for in, want := range map[string]string{"2026-10-17": "2026-27", "2027-03-31": "2026-27", "2027-04-01": "2027-28"} {
		got, err := FinancialYear(in)
		if err != nil || got != want {
			t.Fatalf("FinancialYear(%s) = %s, want %s", in, got, want)
		}
	}
}

// The contract API validates every function signature when the chaincode is built.
func TestChaincodeMetadataIsValid(t *testing.T) {
	if _, err := contractapi.NewChaincode(&InvoiceRegistry{}, &Marketplace{}, &ReceivableToken{}, &Settlement{}); err != nil {
		t.Fatal(err)
	}
}
