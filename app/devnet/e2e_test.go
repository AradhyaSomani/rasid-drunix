package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	sellerGSTIN = "27AAACM1234A1Z5"
	buyerGSTIN  = "29AABCB5678B1Z2"
)

var (
	operator  = Identity{MSP: "NPCIMSP", Role: "operator"}
	riskOrc   = Identity{MSP: "NPCIMSP", Role: "risk_oracle"}
	supplierI = Identity{MSP: "SupplierBankMSP", Role: "supplier", GSTIN: sellerGSTIN}
	buyerI    = Identity{MSP: "BuyerBankMSP", Role: "buyer", GSTIN: buyerGSTIN}
	citi      = Identity{MSP: "CitiMSP", Role: "financier"}
)

type client struct {
	t   *testing.T
	url string
}

func (c client) do(method, path string, as Identity, at string, body any) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.url+path, &buf)
	idj, _ := json.Marshal(as)
	req.Header.Set("X-Rasid-Identity", string(idj))
	if at != "" {
		req.Header.Set("X-Rasid-Time", at)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (c client) tx(fn string, as Identity, at string, args ...any) map[string]any {
	c.t.Helper()
	code, out := c.do("POST", "/tx/"+strings.Replace(fn, ":", "/", 1), as, at, map[string]any{"args": args})
	if code != 200 {
		c.t.Fatalf("%s: %v", fn, out)
	}
	return out
}

func signedQR(t *testing.T) (string, string) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	data, _ := json.Marshal(map[string]any{
		"SellerGstin": sellerGSTIN, "BuyerGstin": buyerGSTIN, "DocNo": "MSME/26-27/0042", "DocTyp": "INV",
		"DocDt": "10/11/2026", "TotInvVal": 1000000.00, "Irn": "irn-0042",
	})
	payload, _ := json.Marshal(map[string]string{"data": string(data)})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","kid":"irp-2026"}`)) + "." + enc.EncodeToString(payload)
	d := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, d[:])
	return signing + "." + enc.EncodeToString(sig), pub
}

// TestLifecycleOverHTTP drives the real contractapi dispatch: string, integer
// and JSON-array arguments, transient data, X.509 attribute identities.
func TestLifecycleOverHTTP(t *testing.T) {
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	s.trustHeaders = true
	ts := httptest.NewServer(s.routes(""))
	defer ts.Close()
	c := client{t, ts.URL}
	day1 := "2026-11-15T10:00:00Z"

	c.tx("InvoiceRegistry:InitLedger", operator, day1, "NPCIMSP", []string{"NPCIMSP"})
	qr, pub := signedQR(t)
	c.tx("InvoiceRegistry:SetIRPPublicKey", operator, day1, "irp-2026", pub)
	reg := c.tx("InvoiceRegistry:RegisterEInvoice", supplierI, day1, qr, "2027-01-09")
	id := reg["result"].(map[string]any)["id"].(string)

	// A failed transaction must leave no trace.
	code, out := c.do("POST", "/tx/InvoiceRegistry/RegisterEInvoice", supplierI, day1, map[string]any{"args": []any{qr, "2027-01-09"}})
	if code == 200 || !strings.Contains(out["error"].(string), "DUPLICATE") {
		t.Fatalf("duplicate accepted: %v", out)
	}

	c.tx("InvoiceRegistry:AcceptInvoice", buyerI, day1, id)
	c.tx("InvoiceRegistry:RecordRiskScore", riskOrc, day1, id, 12, "rasid-graph-0.1", []string{"OK"}, strings.Repeat("a", 64))
	c.tx("Marketplace:ListReceivable", supplierI, day1, id)

	code, bid := c.do("POST", "/bids/"+id, citi, day1, map[string]any{"units": 10000, "rateBps": 1000})
	if code != 200 {
		t.Fatalf("bid: %v", bid)
	}
	reveal := bid["reveal"].(map[string]any)
	code, out = c.do("POST", "/tx/Marketplace/AwardBids", supplierI, day1, map[string]any{
		"args": []any{id}, "transient": map[string]any{"awards": []any{reveal}},
	})
	if code != 200 {
		t.Fatalf("award: %v", out)
	}
	award := out["result"].(map[string]any)["awards"].([]any)[0].(map[string]any)
	funding := award["fundingPaise"].(float64)

	c.tx("Settlement:ConfirmFunding", citi, day1, id, reveal["bidId"], "MOCKR52026000000000001", "RTGS", funding)
	c.tx("Settlement:RegisterMandate", buyerI, day1, id, "UMRN0001", "E_NACH")
	rep := c.tx("Settlement:ConfirmRepayment", operator, "2027-01-09T09:00:00Z", id, "NACH000000000001", 100000000)
	payout := rep["result"].(map[string]any)["payouts"].([]any)[0].(map[string]any)
	closed := c.tx("Settlement:ConfirmPayout", operator, "2027-01-09T10:00:00Z", id, payout["payoutId"], "MOCKR52026000000000002")
	if st := closed["result"].(map[string]any)["status"]; st != "CLOSED" {
		t.Fatalf("status = %v", st)
	}

	code, q := c.do("GET", "/query/InvoiceRegistry/GetReceivable?arg="+id, citi, "", nil)
	if code != 200 || q["status"] != "CLOSED" {
		t.Fatalf("query: %d %v", code, q)
	}
	code, page := c.do("GET", "/query/InvoiceRegistry/ListReceivables?arg=CLOSED&arg=50&arg=", citi, "", nil)
	if code != 200 || len(page["records"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, page)
	}
}
