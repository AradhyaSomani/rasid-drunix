package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// The risk bridge does on the devnet what it does in the risk-oracle org's
// gateway (app/gateway/riskbridge.go): it scores each newly registered
// receivable with the AI risk service and anchors the score on the ledger as
// the risk oracle, and feeds completed repayments back so the model learns
// how each buyer pays.

// riskOracle is the identity the bridge signs as. NPCIMSP must be one of the
// risk-oracle MSPs named in InitLedger, as it is in the demo.
var riskOracle = Identity{MSP: "NPCIMSP", Role: "risk_oracle"}

type receivableView struct {
	ID          string `json:"id"`
	SupplierID  string `json:"supplierId"`
	BuyerID     string `json:"buyerId"`
	AmountPaise int64  `json:"amountPaise"`
	InvoiceDate string `json:"invoiceDate"`
	DueDate     string `json:"dueDate"`
	EInvoice    bool   `json:"eInvoice"`
}

func (s *server) runRiskBridge(riskURL string) {
	c := s.events.subscribe()
	httpc := &http.Client{Timeout: 10 * time.Second}
	for b := range c {
		var ev struct {
			Name    string `json:"name"`
			Payload struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(b, &ev); err != nil {
			continue
		}
		var err error
		switch {
		case ev.Name == "ReceivableRegistered":
			err = s.scoreReceivable(httpc, riskURL, ev.Payload.ID)
		case ev.Name == "RepaymentReceived" && ev.Payload.Status == "REPAID":
			err = s.feedRepayment(httpc, riskURL, ev.Payload.ID)
		}
		if err != nil {
			log.Printf("risk bridge %s %s: %v", ev.Name, ev.Payload.ID, err)
		}
	}
}

func (s *server) receivable(id string) (*receivableView, error) {
	res, err := s.invoke(riskOracle, time.Now().UTC(), "InvoiceRegistry:GetReceivable", []string{id}, nil, false)
	if err != nil {
		return nil, err
	}
	var r receivableView
	return &r, json.Unmarshal(res.Result, &r)
}

func (s *server) scoreReceivable(httpc *http.Client, riskURL, id string) error {
	r, err := s.receivable(id)
	if err != nil {
		return err
	}
	body := map[string]any{"invoice": map[string]any{
		"receivableId": r.ID, "supplierId": r.SupplierID, "buyerId": r.BuyerID,
		"amountPaise": r.AmountPaise, "invoiceDate": r.InvoiceDate, "eInvoice": r.EInvoice,
	}}
	var resp struct {
		Args []string `json:"recordRiskScoreArgs"`
	}
	if err := postJSON(httpc, riskURL+"/score", body, &resp); err != nil {
		return err
	}
	if len(resp.Args) != 5 {
		return fmt.Errorf("risk service returned %d args, want 5", len(resp.Args))
	}
	res, err := s.invoke(riskOracle, time.Now().UTC(), "InvoiceRegistry:RecordRiskScore", resp.Args, nil, true)
	if err != nil {
		return err
	}
	log.Printf("AI risk score %s for %s anchored in tx %s", resp.Args[1], id, res.TxID)
	return nil
}

func (s *server) feedRepayment(httpc *http.Client, riskURL, id string) error {
	r, err := s.receivable(id)
	if err != nil {
		return err
	}
	body := map[string]any{"repayments": []map[string]string{{
		"buyerId": r.BuyerID, "dueDate": r.DueDate, "paidDate": time.Now().UTC().Format("2006-01-02"),
	}}}
	return postJSON(httpc, riskURL+"/repayments", body, nil)
}

func postJSON(httpc *http.Client, url string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	resp, err := httpc.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
