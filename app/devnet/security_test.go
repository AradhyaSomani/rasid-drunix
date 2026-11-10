package main

// Security and access-control suite. These tests pin down who may do what,
// over HTTP, against the real chaincode. If one fails after a change, the
// change has opened a hole: fix the code, not the test.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	nbfc          = Identity{MSP: "NbfcMSP", Role: "financier"}
	otherSupplier = Identity{MSP: "SupplierBankMSP", Role: "supplier", GSTIN: "27AAACX9999X1Z1"}
	otherBuyer    = Identity{MSP: "BuyerBankMSP", Role: "buyer", GSTIN: "29AABCZ1111Z1Z9"}
	rogueOperator = Identity{MSP: "CitiMSP", Role: "operator"}    // a member bank's CA minting an operator
	rogueRisk     = Identity{MSP: "CitiMSP", Role: "risk_oracle"} // ... or a risk oracle
	noRole        = Identity{MSP: "CitiMSP", Role: "admin"}
)

// everyone is every identity the matrix tries, by name.
var everyone = map[string]Identity{
	"operator": operator, "risk": riskOrc, "supplier": supplierI, "buyer": buyerI, "citi": citi, "nbfc": nbfc,
	"otherSupplier": otherSupplier, "otherBuyer": otherBuyer, "rogueOperator": rogueOperator, "rogueRisk": rogueRisk, "noRole": noRole,
}

func newTestServer(t *testing.T, trust bool) (*server, *httptest.Server) {
	t.Helper()
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	s.trustHeaders, s.quiet = trust, true
	ts := httptest.NewServer(s.routes(""))
	t.Cleanup(ts.Close)
	return s, ts
}

// registered sets up the network and registers one e-invoice; it returns its ID.
func registered(t *testing.T, c client) (id, qr string) {
	t.Helper()
	day := time.Now().UTC().Format(time.RFC3339)
	c.tx("InvoiceRegistry:InitLedger", operator, day, "NPCIMSP", []string{"NPCIMSP"})
	qr, pub := signedQRDated(t, time.Now(), "MSME/26-27/0042")
	c.tx("InvoiceRegistry:SetIRPPublicKey", operator, day, "irp-2026", pub)
	reg := c.tx("InvoiceRegistry:RegisterEInvoice", supplierI, day, qr, time.Now().AddDate(0, 0, 60).Format("2006-01-02"))
	return reg["result"].(map[string]any)["id"].(string), qr
}

// TestRoleMatrix calls every state-changing function as every identity the
// access rules forbid, and expects 403 each time. A refused call must also
// leave the ledger as it was.
func TestRoleMatrix(t *testing.T) {
	s, ts := newTestServer(t, true)
	c := client{t, ts.URL}
	id, qr := registered(t, c)
	due := time.Now().AddDate(0, 0, 60).Format("2006-01-02")
	_, pub := signedQRDated(t, time.Now(), "X")

	cases := []struct {
		fn      string
		args    []any
		allowed []string
	}{
		{"InvoiceRegistry:SetIRPPublicKey", []any{"irp-x", pub}, []string{"operator"}},
		{"InvoiceRegistry:SetRiskThresholds", []any{70, 50}, []string{"operator"}},
		{"InvoiceRegistry:RegisterEInvoice", []any{qr, due}, []string{"supplier"}},
		{"InvoiceRegistry:AcceptInvoice", []any{id}, []string{"buyer"}},
		{"InvoiceRegistry:RejectInvoice", []any{id, "NOT_RECOGNISED"}, []string{"buyer"}},
		{"InvoiceRegistry:CancelInvoice", []any{id}, []string{"supplier"}},
		{"InvoiceRegistry:RecordRiskScore", []any{id, 10, "m", []string{"OK"}, strings.Repeat("a", 64)}, []string{"risk"}},
		{"Marketplace:ListReceivable", []any{id}, []string{"supplier"}},
		{"Marketplace:AwardBids", []any{id}, []string{"supplier"}},
		{"Marketplace:WithdrawBid", []any{id, "devtx000001"}, []string{"citi", "nbfc"}},
		{"Settlement:ConfirmFunding", []any{id, "devtx000001", "UTR1", "RTGS", 1}, []string{"citi", "nbfc"}},
		{"Settlement:ExpireUnfundedAwards", []any{id}, []string{"supplier", "operator"}},
		{"Settlement:RegisterMandate", []any{id, "UMRN1", "E_NACH"}, []string{"buyer"}},
		{"Settlement:ConfirmRepayment", []any{id, "UTR2", 1}, []string{"buyer", "operator"}},
		{"Settlement:ConfirmPayout", []any{id, "p1", "UTR3"}, []string{"operator"}},
		{"Settlement:MarkDefaulted", []any{id}, []string{"operator"}},
		{"ReceivableToken:TransferUnits", []any{id, "NbfcMSP", 1}, []string{"citi", "nbfc"}},
	}
	before := snapshot(s)
	for _, tc := range cases {
		for name, who := range everyone {
			if contains(tc.allowed, name) {
				continue
			}
			code, out := c.do("POST", "/tx/"+strings.Replace(tc.fn, ":", "/", 1), who, "", map[string]any{"args": tc.args})
			if code != http.StatusForbidden {
				t.Errorf("%s as %s: status %d (%v), want 403", tc.fn, name, code, out["error"])
			}
		}
	}
	// Sealed bids and bid reveals are for financiers only.
	for name, who := range everyone {
		if name == "citi" || name == "nbfc" {
			continue
		}
		if code, _ := c.do("POST", "/bids/"+id, who, "", map[string]any{"units": 10000, "rateBps": 1000}); code != http.StatusForbidden {
			t.Errorf("bid as %s: status %d, want 403", name, code)
		}
		if code, _ := c.do("GET", "/query/Marketplace/GetMyBid?arg="+id+"&arg=x", who, "", nil); code != http.StatusForbidden {
			t.Errorf("GetMyBid as %s: status %d, want 403", name, code)
		}
	}
	// Nobody can take over the network once it is set up.
	for name, who := range everyone {
		if code, _ := c.do("POST", "/tx/InvoiceRegistry/InitLedger", who, "", map[string]any{"args": []any{who.MSP, []string{who.MSP}}}); code == http.StatusOK {
			t.Errorf("InitLedger ran again as %s", name)
		}
	}
	if after := snapshot(s); after != before {
		t.Fatal("a refused call changed the ledger")
	}
}

// TestBidPrivacy: a bank's rate never reaches another bank, and the public
// ledger holds only the hash.
func TestBidPrivacy(t *testing.T) {
	_, ts := newTestServer(t, true)
	c := client{t, ts.URL}
	id, _ := registered(t, c)
	c.tx("InvoiceRegistry:AcceptInvoice", buyerI, "", id)
	c.tx("InvoiceRegistry:RecordRiskScore", riskOrc, "", id, 12, "m", []string{"OK"}, strings.Repeat("a", 64))
	c.tx("Marketplace:ListReceivable", supplierI, "", id)
	code, bid := c.do("POST", "/bids/"+id, citi, "", map[string]any{"units": 6000, "rateBps": 1037})
	if code != 200 {
		t.Fatalf("bid: %v", bid)
	}
	bidID := bid["reveal"].(map[string]any)["bidId"].(string)

	if code, out := c.do("GET", "/query/Marketplace/GetMyBid?arg="+id+"&arg="+bidID, citi, "", nil); code != 200 || out["rateBps"] != float64(1037) {
		t.Fatalf("citi cannot read its own bid: %d %v", code, out)
	}
	code, out := c.do("GET", "/query/Marketplace/GetMyBid?arg="+id+"&arg="+bidID, nbfc, "", nil)
	if code == 200 || strings.Contains(fmt.Sprint(out), "1037") {
		t.Fatalf("the NBFC read Citi's bid: %d %v", code, out)
	}
	raw := rawGet(t, ts.URL+"/query/Marketplace/ListBids?arg="+id, nbfc)
	if strings.Contains(raw, "1037") || strings.Contains(raw, "rateBps") || strings.Contains(raw, "salt") {
		t.Fatalf("public bid list leaks the rate: %s", raw)
	}
	// Nor can a bank withdraw another bank's bid.
	if code, _ := c.do("POST", "/tx/Marketplace/WithdrawBid", nbfc, "", map[string]any{"args": []any{id, bidID}}); code == 200 {
		t.Fatal("the NBFC withdrew Citi's bid")
	}
}

// TestSignInRequired: without -trust-headers every ledger endpoint needs a
// session, and the identity header is ignored.
func TestSignInRequired(t *testing.T) {
	s, ts := newTestServer(t, false)
	s.irp, _ = newDemoIRP()
	ts.Config.Handler = s.routes("")
	hdr, _ := json.Marshal(operator)
	for _, ep := range []struct{ method, path string }{
		{"POST", "/tx/InvoiceRegistry/InitLedger"}, {"GET", "/query/InvoiceRegistry/ListReceivables"},
		{"POST", "/bids/x"}, {"GET", "/events"}, {"GET", "/demo/irp-key"}, {"POST", "/demo/invoice"}, {"GET", "/auth/me"},
	} {
		for _, header := range []bool{false, true} {
			req, _ := http.NewRequest(ep.method, ts.URL+ep.path, strings.NewReader(`{"args":[]}`))
			if header {
				req.Header.Set("X-Rasid-Identity", string(hdr))
			}
			req.Header.Set("Authorization", "Bearer not-a-real-token")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s (identity header %v): %d, want 401", ep.method, ep.path, header, resp.StatusCode)
			}
		}
	}
	for _, path := range []string{"/health", "/auth/accounts"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
		if strings.Contains(string(body), `"password"`) {
			t.Errorf("%s leaks a password: %s", path, body)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, ts := newTestServer(t, false)
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	want := map[string]string{
		"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer",
	}
	for k, v := range want {
		if got := resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, part := range []string{"default-src 'self'", "script-src 'self';", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, part) {
			t.Errorf("CSP %q lacks %q", csp, part)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Error("CSP allows inline scripts")
	}
}

func TestOversizedBodyRefused(t *testing.T) {
	_, ts := newTestServer(t, true)
	big := `{"args":["` + strings.Repeat("a", 2<<20) + `"]}`
	req, _ := http.NewRequest("POST", ts.URL+"/tx/InvoiceRegistry/RegisterEInvoice", strings.NewReader(big))
	req.Header.Set("X-Rasid-Identity", `{"msp":"SupplierBankMSP","role":"supplier","gstin":"27AAACM1234A1Z5"}`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("2 MB body: %d, want 413", resp.StatusCode)
	}
	resp, err = http.Post(ts.URL+"/auth/login", "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d, want 400", resp.StatusCode)
	}
}

// TestLoginThrottle: five wrong passwords lock the username, even against
// the right password, until the lockout passes.
func TestLoginThrottle(t *testing.T) {
	s, ts := newTestServer(t, false)
	s.auth.lockout = 200 * time.Millisecond
	login := func(pass string) *http.Response {
		b, _ := json.Marshal(map[string]string{"username": "citi", "password": pass})
		resp, err := http.Post(ts.URL+"/auth/login", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for i := 0; i < maxFails; i++ {
		if r := login("guess"); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: %d", i, r.StatusCode)
		}
	}
	r := login(DemoPassword)
	if r.StatusCode != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Fatalf("after %d misses: %d, want 429 with Retry-After", maxFails, r.StatusCode)
	}
	// Another username is not affected.
	b, _ := json.Marshal(map[string]string{"username": "nbfc", "password": DemoPassword})
	if resp, _ := http.Post(ts.URL+"/auth/login", "application/json", bytes.NewReader(b)); resp.StatusCode != 200 {
		t.Fatalf("nbfc locked out too: %d", resp.StatusCode)
	}
	time.Sleep(250 * time.Millisecond)
	if r := login(DemoPassword); r.StatusCode != 200 {
		t.Fatalf("after the lockout: %d, want 200", r.StatusCode)
	}
}

// TestRefusalStatuses: a client can tell a duplicate (409) from a missing
// invoice (404) and a forbidden call (403).
func TestRefusalStatuses(t *testing.T) {
	_, ts := newTestServer(t, true)
	c := client{t, ts.URL}
	id, qr := registered(t, c)
	due := time.Now().AddDate(0, 0, 60).Format("2006-01-02")
	if code, _ := c.do("POST", "/tx/InvoiceRegistry/RegisterEInvoice", supplierI, "", map[string]any{"args": []any{qr, due}}); code != http.StatusConflict {
		t.Errorf("duplicate: %d, want 409", code)
	}
	if code, _ := c.do("GET", "/query/InvoiceRegistry/GetReceivable?arg=nope", citi, "", nil); code != http.StatusNotFound {
		t.Errorf("missing: %d, want 404", code)
	}
	if code, _ := c.do("POST", "/tx/Marketplace/ListReceivable", supplierI, "", map[string]any{"args": []any{id}}); code != http.StatusConflict {
		t.Errorf("listing before acceptance: %d, want 409", code)
	}
	// A score over the limit blocks listing.
	c.tx("InvoiceRegistry:AcceptInvoice", buyerI, "", id)
	c.tx("InvoiceRegistry:RecordRiskScore", riskOrc, "", id, 85, "m", []string{"NEAR_DUPLICATE"}, strings.Repeat("b", 64))
	code, out := c.do("POST", "/tx/Marketplace/ListReceivable", supplierI, "", map[string]any{"args": []any{id}})
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["error"]), "exceeds listing limit") {
		t.Errorf("risky listing: %d %v, want 409", code, out)
	}
}

func TestChaincodeStatus(t *testing.T) {
	for msg, want := range map[string]int{
		"role buyer may not call this function (allowed: [supplier])": 403,
		"CitiMSP is not an authorised operator organisation":          403,
		"caller is not the buyer on receivable x":                     403,
		"receivable x does not exist":                                 404,
		"DUPLICATE: invoice already registered as receivable y":       409,
		"receivable x: cannot move from LISTED to ACCEPTED":           409,
		"risk score 85 exceeds listing limit 70 (reasons [X])":        409,
		"units must be 1-10000":                                       400,
	} {
		if got := chaincodeStatus(msg); got != want {
			t.Errorf("%q: %d, want %d", msg, got, want)
		}
	}
}

// TestLedgerTrail: the devnet keeps history like a peer, so the console can
// show every state change with its transaction.
func TestLedgerTrail(t *testing.T) {
	_, ts := newTestServer(t, true)
	c := client{t, ts.URL}
	id, _ := registered(t, c)
	c.tx("InvoiceRegistry:AcceptInvoice", buyerI, "", id)
	c.tx("InvoiceRegistry:RecordRiskScore", riskOrc, "", id, 12, "m", []string{"OK"}, strings.Repeat("a", 64))
	c.tx("Marketplace:ListReceivable", supplierI, "", id)
	raw := rawGet(t, ts.URL+"/query/InvoiceRegistry/GetReceivableHistory?arg="+id, citi)
	var trail []struct {
		TxID       string `json:"txId"`
		Timestamp  string `json:"timestamp"`
		Receivable struct {
			Status string `json:"status"`
		} `json:"receivable"`
	}
	if err := json.Unmarshal([]byte(raw), &trail); err != nil {
		t.Fatalf("%v: %s", err, raw)
	}
	var got []string
	for _, v := range trail {
		if v.TxID == "" || v.Timestamp == "" {
			t.Fatalf("version without tx or time: %s", raw)
		}
		got = append(got, v.Receivable.Status)
	}
	if strings.Join(got, ",") != "REGISTERED,ACCEPTED,ACCEPTED,LISTED" {
		t.Fatalf("trail = %v", got)
	}
}

// TestDemoIRP: the built-in IRP lets a supplier raise a signed invoice for
// its own GSTIN only, and the endpoints exist only with -demo-irp.
func TestDemoIRP(t *testing.T) {
	s, ts := newTestServer(t, true)
	c := client{t, ts.URL}
	if code, _ := c.do("POST", "/demo/invoice", supplierI, "", nil); code != http.StatusNotFound {
		t.Fatalf("demo endpoint without -demo-irp: %d, want 404", code)
	}
	s.irp, _ = newDemoIRP()
	ts.Config.Handler = s.routes("")

	day := time.Now().UTC().Format(time.RFC3339)
	c.tx("InvoiceRegistry:InitLedger", operator, day, "NPCIMSP", []string{"NPCIMSP"})
	_, key := c.do("GET", "/demo/irp-key", operator, "", nil)
	c.tx("InvoiceRegistry:SetIRPPublicKey", operator, day, key["kid"], key["pem"])
	if code, _ := c.do("POST", "/demo/invoice", buyerI, "", map[string]any{}); code != http.StatusForbidden {
		t.Fatalf("buyer raised an invoice: %d", code)
	}
	if code, _ := c.do("POST", "/demo/invoice", supplierI, "", map[string]any{"docNo": "<script>"}); code != http.StatusBadRequest {
		t.Fatalf("markup in the document number: %d, want 400", code)
	}
	code, inv := c.do("POST", "/demo/invoice", supplierI, "", map[string]any{"rupees": 250000})
	if code != 200 {
		t.Fatalf("sample invoice: %d %v", code, inv)
	}
	due := time.Now().AddDate(0, 0, 45).Format("2006-01-02")
	reg := c.tx("InvoiceRegistry:RegisterEInvoice", supplierI, day, inv["qr"], due)
	r := reg["result"].(map[string]any)
	if r["amountPaise"] != float64(25000000) || r["eInvoice"] != true {
		t.Fatalf("registered = %v", r)
	}
	// Another supplier cannot register it: the seller GSTIN is signed in.
	if code, _ := c.do("POST", "/tx/InvoiceRegistry/RegisterEInvoice", otherSupplier, "", map[string]any{"args": []any{inv["qr"], due}}); code != http.StatusForbidden {
		t.Fatalf("other supplier registered it: %d", code)
	}
}

// TestHealth reports the ledger height the console shows.
func TestHealth(t *testing.T) {
	_, ts := newTestServer(t, true)
	c := client{t, ts.URL}
	registered(t, c)
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&h)
	if h["status"] != "ok" || h["height"] != float64(3) || h["aiRisk"] != "off" {
		t.Fatalf("health = %v", h)
	}
}

func snapshot(s *server) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := json.Marshal([]any{s.world.state, s.world.private, s.world.height})
	return string(b)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func rawGet(t *testing.T, url string, as Identity) string {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	idj, _ := json.Marshal(as)
	req.Header.Set("X-Rasid-Identity", string(idj))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
