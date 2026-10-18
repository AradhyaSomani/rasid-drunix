package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"

	"github.com/hyperledger/fabric-gateway/pkg/client"
)

// App holds the gateway connection and the HTTP handlers.
type App struct {
	cfg      config
	network  *client.Network
	contract *client.Contract
	events   *hub
	auth     *auth // nil: no login (scripts, adapters, the risk bridge)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "msp": a.cfg.MSPID})
	})
	guard := func(h http.HandlerFunc) http.HandlerFunc { return h }
	if a.auth != nil {
		guard = a.auth.require
		a.auth.routes(mux)
	}
	mux.HandleFunc("POST /tx/{contract}/{function}", guard(a.handleSubmit))
	mux.HandleFunc("GET /query/{contract}/{function}", guard(a.handleQuery))
	mux.HandleFunc("POST /bids/{receivableId}", guard(a.handleBid))
	mux.HandleFunc("GET /events", guard(a.handleEvents))
	if a.cfg.WebDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(a.cfg.WebDir)))
	}
	return withCORS(a.cfg.CORSOrigin, mux)
}

// withCORS lets the web console, served from one gateway, call the others.
func withCORS(origin string, next http.Handler) http.Handler {
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type txRequest struct {
	Args      []any                      `json:"args"`
	Transient map[string]json.RawMessage `json:"transient"`
	// EndorsingOrgs pins which organisations may see transient data. The
	// gateway refuses to send transient data to orgs outside the private
	// collection unless the client names them.
	EndorsingOrgs []string `json:"endorsingOrgs"`
}

type txResult struct {
	TxID   string          `json:"txId"`
	Result json.RawMessage `json:"result,omitempty"`
}

// submit endorses and commits a transaction and returns its ID with the result.
func (a *App) submit(name string, args []string, transient map[string][]byte, endorsingOrgs ...string) (*txResult, error) {
	opts := []client.ProposalOption{client.WithArguments(args...)}
	if len(transient) > 0 {
		opts = append(opts, client.WithTransient(transient))
	}
	if len(endorsingOrgs) > 0 {
		opts = append(opts, client.WithEndorsingOrganizations(endorsingOrgs...))
	}
	proposal, err := a.contract.NewProposal(name, opts...)
	if err != nil {
		return nil, err
	}
	tx, err := proposal.Endorse()
	if err != nil {
		return nil, err
	}
	commit, err := tx.Submit()
	if err != nil {
		return nil, err
	}
	status, err := commit.Status()
	if err != nil {
		return nil, err
	}
	if !status.Successful {
		return nil, fmt.Errorf("transaction %s failed to commit: %s", status.TransactionID, status.Code)
	}
	return &txResult{TxID: tx.TransactionID(), Result: asJSON(tx.Result())}, nil
}

func (a *App) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req txRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	args, err := stringArgs(req.Args)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	transient := map[string][]byte{}
	for k, v := range req.Transient {
		transient[k] = v
	}
	res, err := a.submit(r.PathValue("contract")+":"+r.PathValue("function"), args, transient, req.EndorsingOrgs...)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *App) handleQuery(w http.ResponseWriter, r *http.Request) {
	out, err := a.contract.EvaluateTransaction(r.PathValue("contract")+":"+r.PathValue("function"), r.URL.Query()["arg"]...)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(asJSON(out))
}

// handleBid places a sealed bid with a random salt. The response holds the
// salt: the bidder keeps it and shares it with the supplier's platform
// off-chain to reveal the bid at award time.
func (a *App) handleBid(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Units   int64 `json:"units"`
		RateBps int64 `json:"rateBps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	bid := map[string]any{"units": req.Units, "rateBps": req.RateBps, "salt": hex.EncodeToString(salt)}
	raw, _ := json.Marshal(bid)
	// The bid goes into this org's implicit collection, so the gateway only
	// shares it with orgs we name: ourselves and the network operator, whose
	// endorsement the chaincode policy requires.
	orgs := []string{a.cfg.MSPID}
	if a.cfg.OperatorMSP != a.cfg.MSPID {
		orgs = append(orgs, a.cfg.OperatorMSP)
	}
	res, err := a.submit("Marketplace:SubmitBid", []string{r.PathValue("receivableId")}, map[string][]byte{"bid": raw}, orgs...)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	bid["bidId"] = res.TxID
	writeJSON(w, http.StatusOK, map[string]any{"reveal": bid, "commitment": res.Result})
}

// stringArgs turns JSON values into chaincode string arguments: strings pass
// through, numbers and arrays are sent as their JSON text.
func stringArgs(in []any) ([]string, error) {
	out := make([]string, len(in))
	for i, v := range in {
		switch x := v.(type) {
		case string:
			out[i] = x
		case float64:
			out[i] = strconv.FormatFloat(x, 'f', -1, 64)
		default:
			b, err := json.Marshal(x)
			if err != nil {
				return nil, err
			}
			out[i] = string(b)
		}
	}
	return out, nil
}

func asJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	if json.Valid(b) {
		return b
	}
	q, _ := json.Marshal(string(b))
	return q
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	msg := err.Error()
	var endorseErr *client.EndorseError
	if errors.As(err, &endorseErr) {
		if details := endorseErr.GRPCStatus().Details(); len(details) > 0 {
			msg = fmt.Sprintf("%s: %v", msg, details)
		}
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// hub fans chaincode events out to every connected SSE client.
type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newHub() *hub { return &hub{subs: map[chan []byte]struct{}{}} }

func (h *hub) publish(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- b:
		default: // slow client: drop rather than block the ledger stream
		}
	}
}

func (h *hub) subscribe() chan []byte {
	c := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *hub) unsubscribe(c chan []byte) {
	h.mu.Lock()
	delete(h.subs, c)
	h.mu.Unlock()
}

func (a *App) streamEvents(ctx context.Context) {
	events, err := a.network.ChaincodeEvents(ctx, a.cfg.Chaincode)
	if err != nil {
		log.Printf("chaincode events: %v", err)
		return
	}
	for ev := range events {
		b, _ := json.Marshal(map[string]any{
			"block": ev.BlockNumber, "txId": ev.TransactionID, "name": ev.EventName, "payload": asJSON(ev.Payload),
		})
		a.events.publish(b)
	}
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush() // send headers now, so browsers accept the stream before the first event
	c := a.events.subscribe()
	defer a.events.unsubscribe(c)
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-c:
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}
