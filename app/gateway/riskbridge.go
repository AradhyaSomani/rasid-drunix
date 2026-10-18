package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// receivableView is the subset of the on-ledger receivable the bridge needs.
type receivableView struct {
	ID          string `json:"id"`
	SupplierID  string `json:"supplierId"`
	BuyerID     string `json:"buyerId"`
	AmountPaise int64  `json:"amountPaise"`
	InvoiceDate string `json:"invoiceDate"`
	DueDate     string `json:"dueDate"`
	EInvoice    bool   `json:"eInvoice"`
	Status      string `json:"status"`
}

// runRiskBridge listens for new receivables, scores them with the risk
// service and anchors the score on the ledger. It also feeds completed
// repayments back so buyer payment behaviour is learned. Runs only in the
// risk-oracle org's gateway.
func (a *App) runRiskBridge(ctx context.Context) {
	c := a.events.subscribe()
	defer a.events.unsubscribe(c)
	httpc := &http.Client{Timeout: 10 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-c:
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
				err = a.scoreReceivable(httpc, ev.Payload.ID)
			case ev.Name == "RepaymentReceived" && ev.Payload.Status == "REPAID":
				err = a.feedRepayment(httpc, ev.Payload.ID)
			}
			if err != nil {
				log.Printf("risk bridge %s %s: %v", ev.Name, ev.Payload.ID, err)
			}
		}
	}
}

func (a *App) getReceivable(id string) (*receivableView, error) {
	out, err := a.contract.EvaluateTransaction("InvoiceRegistry:GetReceivable", id)
	if err != nil {
		return nil, err
	}
	var r receivableView
	return &r, json.Unmarshal(out, &r)
}

func (a *App) scoreReceivable(httpc *http.Client, id string) error {
	r, err := a.getReceivable(id)
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
	if err := postJSON(httpc, a.cfg.RiskURL+"/score", body, &resp); err != nil {
		return err
	}
	if len(resp.Args) != 5 {
		return fmt.Errorf("risk service returned %d args, want 5", len(resp.Args))
	}
	res, err := a.submit("InvoiceRegistry:RecordRiskScore", resp.Args, nil)
	if err != nil {
		return err
	}
	log.Printf("risk score %s for %s anchored in tx %s", resp.Args[1], id, res.TxID)
	return nil
}

func (a *App) feedRepayment(httpc *http.Client, id string) error {
	r, err := a.getReceivable(id)
	if err != nil {
		return err
	}
	body := map[string]any{"repayments": []map[string]string{{
		"buyerId": r.BuyerID, "dueDate": r.DueDate, "paidDate": time.Now().UTC().Format("2006-01-02"),
	}}}
	return postJSON(httpc, a.cfg.RiskURL+"/repayments", body, nil)
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
