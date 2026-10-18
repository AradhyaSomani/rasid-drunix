package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRiskBridgeScoresNewInvoices checks that a registered invoice is scored
// by the AI service and the score lands on the ledger as the risk oracle's.
func TestRiskBridgeScoresNewInvoices(t *testing.T) {
	var asked string
	ai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Invoice struct {
				ReceivableID string `json:"receivableId"`
			} `json:"invoice"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		asked = body.Invoice.ReceivableID
		_ = json.NewEncoder(w).Encode(map[string]any{"recordRiskScoreArgs": []string{
			body.Invoice.ReceivableID, "7", "rasid-graph-0.1", `["ROUND_AMOUNT"]`, strings.Repeat("b", 64)}})
	}))
	defer ai.Close()

	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	s.trustHeaders = true
	go s.runRiskBridge(ai.URL)
	ts := httptest.NewServer(s.routes(""))
	defer ts.Close()
	c := client{t, ts.URL}
	day1 := "2026-11-15T10:00:00Z"
	c.tx("InvoiceRegistry:InitLedger", operator, day1, "NPCIMSP", []string{"NPCIMSP"})
	qr, pub := signedQR(t)
	c.tx("InvoiceRegistry:SetIRPPublicKey", operator, day1, "irp-2026", pub)
	id := c.tx("InvoiceRegistry:RegisterEInvoice", supplierI, day1, qr, "2027-01-09")["result"].(map[string]any)["id"].(string)

	for i := 0; i < 50; i++ {
		_, r := c.do("GET", "/query/InvoiceRegistry/GetReceivable?arg="+id, citi, "", nil)
		if risk, ok := r["risk"].(map[string]any); ok {
			if risk["score"].(float64) != 7 || risk["scoredBy"] != "NPCIMSP" || risk["modelVersion"] != "rasid-graph-0.1" {
				t.Fatalf("risk = %v", risk)
			}
			if asked != id {
				t.Fatalf("AI service asked about %q, want %q", asked, id)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("invoice was never scored")
}
