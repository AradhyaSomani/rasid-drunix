package contracts

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Ledger key prefixes (composite key object types).
const (
	keyReceivable = "receivable" // receivable~<id>
	keyIRN        = "irn"        // irn~<irnHash> -> receivable id
	keyIRPKey     = "irpkey"     // irpkey~<kid> -> PEM public key
	keyBid        = "bid"        // bid~<receivableId>~<bidId>
	keyHolding    = "holding"    // holding~<receivableId>~<holderId>
	keyUTR        = "utr"        // utr~<utr> -> receivable id (each UTR usable once)
	keyConfig     = "config"     // config~network, config~risk
)

const dateLayout = "2006-01-02"

func compositeKey(ctx contractapi.TransactionContextInterface, objectType string, attrs ...string) (string, error) {
	return ctx.GetStub().CreateCompositeKey(objectType, attrs)
}

func getJSON(ctx contractapi.TransactionContextInterface, key string, v any) (bool, error) {
	b, err := ctx.GetStub().GetState(key)
	if err != nil {
		return false, fmt.Errorf("read %q: %w", key, err)
	}
	if b == nil {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}

func putJSON(ctx contractapi.TransactionContextInterface, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return ctx.GetStub().PutState(key, b)
}

func getReceivable(ctx contractapi.TransactionContextInterface, id string) (*Receivable, error) {
	key, err := compositeKey(ctx, keyReceivable, id)
	if err != nil {
		return nil, err
	}
	var r Receivable
	found, err := getJSON(ctx, key, &r)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("receivable %s does not exist", id)
	}
	return &r, nil
}

func putReceivable(ctx contractapi.TransactionContextInterface, r *Receivable) error {
	now, err := txTime(ctx)
	if err != nil {
		return err
	}
	r.DocType = keyReceivable
	r.UpdatedAt = now.Format(time.RFC3339)
	key, err := compositeKey(ctx, keyReceivable, r.ID)
	if err != nil {
		return err
	}
	return putJSON(ctx, key, r)
}

// txTime is the client-signed transaction timestamp, identical on every peer.
func txTime(ctx contractapi.TransactionContextInterface) (time.Time, error) {
	ts, err := ctx.GetStub().GetTxTimestamp()
	if err != nil {
		return time.Time{}, err
	}
	return ts.AsTime().UTC(), nil
}

func txDate(ctx contractapi.TransactionContextInterface) (time.Time, error) {
	t, err := txTime(ctx)
	if err != nil {
		return t, err
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
}

func parseDate(s string) (time.Time, error) {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return t, fmt.Errorf("date %q must be YYYY-MM-DD", s)
	}
	return t, nil
}

func daysBetween(from, to time.Time) int64 {
	return int64(to.Sub(from).Hours() / 24)
}

// claimUTR records a payment reference so the same UTR can never be posted twice.
func claimUTR(ctx contractapi.TransactionContextInterface, utr, receivableID string) error {
	if utr == "" {
		return fmt.Errorf("UTR is required")
	}
	key, err := compositeKey(ctx, keyUTR, utr)
	if err != nil {
		return err
	}
	existing, err := ctx.GetStub().GetState(key)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("UTR %s was already used for receivable %s", utr, existing)
	}
	return ctx.GetStub().PutState(key, []byte(receivableID))
}

// emit sets the transaction's chaincode event. Fabric keeps only one event per
// transaction, so each function emits a single event carrying everything
// off-chain adapters need.
func emit(ctx contractapi.TransactionContextInterface, name string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return ctx.GetStub().SetEvent(name, b)
}

// NetworkConfig names the organisations trusted for privileged roles.
type NetworkConfig struct {
	OperatorMSP    string   `json:"operatorMsp"`
	RiskOracleMSPs []string `json:"riskOracleMsps"`
}

func getNetworkConfig(ctx contractapi.TransactionContextInterface) (*NetworkConfig, error) {
	key, err := compositeKey(ctx, keyConfig, "network")
	if err != nil {
		return nil, err
	}
	var cfg NetworkConfig
	found, err := getJSON(ctx, key, &cfg)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("network not initialised: call InvoiceRegistry:InitLedger first")
	}
	return &cfg, nil
}

// RiskConfig holds the maximum risk score allowed for listing.
type RiskConfig struct {
	MaxScoreEInvoice int `json:"maxScoreEInvoice"`
	MaxScoreManual   int `json:"maxScoreManual"`
}

var defaultRiskConfig = RiskConfig{MaxScoreEInvoice: 70, MaxScoreManual: 50}

func getRiskConfig(ctx contractapi.TransactionContextInterface) (RiskConfig, error) {
	key, err := compositeKey(ctx, keyConfig, "risk")
	if err != nil {
		return RiskConfig{}, err
	}
	cfg := defaultRiskConfig
	_, err = getJSON(ctx, key, &cfg)
	return cfg, err
}
