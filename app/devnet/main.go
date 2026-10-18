// Command devnet runs the real RASID chaincode against an in-memory ledger
// behind the same REST API as app/gateway, so the risk service, settlement
// adapters and web demo can be developed before the DRUNIX network is up.
//
// Users sign in (POST /auth/login) and every request carries the session
// token, so the server, not the browser, decides which ledger identity acts.
// Scripts and tests may instead name the identity in an X-Rasid-Identity
// header, e.g. {"msp":"CitiMSP","role":"financier"}, but only when devnet runs
// with -trust-headers. X-Rasid-Time (RFC 3339) overrides the transaction time,
// to demo overdue and default without waiting. With -risk-url, the AI risk
// service scores every new invoice, as the risk bridge does on a live network.
//
//	go run . -listen :8080        then open http://localhost:8080 for the web console
//
// Not a DRUNIX replacement: one process, no endorsement, no ordering, no MVCC.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/AradhyaSomani/rasid-drunix/chaincode/rasid/contracts"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

type server struct {
	mu           sync.Mutex
	cc           *contractapi.ContractChaincode
	world        *world
	txSeq        int
	events       *hub
	auth         *auth
	trustHeaders bool // accept X-Rasid-Identity without a login (scripts, tests)
}

func newServer() (*server, error) {
	registry := &contracts.InvoiceRegistry{}
	registry.Name = "InvoiceRegistry"
	market := &contracts.Marketplace{}
	market.Name = "Marketplace"
	token := &contracts.ReceivableToken{}
	token.Name = "ReceivableToken"
	settlement := &contracts.Settlement{}
	settlement.Name = "Settlement"
	cc, err := contractapi.NewChaincode(registry, market, token, settlement)
	if err != nil {
		return nil, err
	}
	return &server{
		cc:     cc,
		world:  &world{state: map[string][]byte{}, private: map[string]map[string][]byte{}},
		events: newHub(),
		auth:   newAuth(demoAccounts(DemoPassword), true),
	}, nil
}

type txResult struct {
	TxID   string          `json:"txId"`
	Result json.RawMessage `json:"result,omitempty"`
}

// invoke runs one transaction through contractapi's real dispatch (argument
// parsing, identity checks, metadata validation). submit=true commits writes
// and publishes the event when the chaincode returns success.
func (s *server) invoke(id Identity, at time.Time, fn string, args []string, transient map[string][]byte, submit bool) (*txResult, error) {
	creator, err := creatorFor(id)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	txID := "devquery"
	if submit {
		s.txSeq++
		txID = fmt.Sprintf("devtx%06d", s.txSeq)
	}
	stub := &txStub{
		w: s.world, txID: txID, fn: fn, args: args, creator: creator,
		transient: transient, now: at, writes: map[string][]byte{}, pvtWrites: map[string]map[string][]byte{},
	}
	resp := s.cc.Invoke(stub)
	if resp.Status != 200 {
		return nil, fmt.Errorf("%s", resp.Message)
	}
	if submit {
		stub.commit()
		if stub.eventName != "" {
			b, _ := json.Marshal(map[string]any{
				"block": s.txSeq, "txId": stub.txID, "name": stub.eventName, "payload": json.RawMessage(stub.eventBody),
			})
			s.events.publish(b)
		}
	}
	return &txResult{TxID: stub.txID, Result: asJSON(resp.Payload)}, nil
}

// identify returns who the request acts as: the signed-in account, or, with
// -trust-headers only, the X-Rasid-Identity header.
func (s *server) identify(r *http.Request) (Identity, time.Time, error) {
	var id Identity
	if acct, ok := s.auth.lookup(bearerToken(r)); ok {
		id = acct.Identity
	} else if s.trustHeaders && r.Header.Get("X-Rasid-Identity") != "" {
		if err := json.Unmarshal([]byte(r.Header.Get("X-Rasid-Identity")), &id); err != nil {
			return id, time.Time{}, fmt.Errorf("X-Rasid-Identity header must be JSON like {\"msp\":\"CitiMSP\",\"role\":\"financier\"}")
		}
	} else {
		return id, time.Time{}, errUnauthorized
	}
	at := time.Now().UTC()
	if v := r.Header.Get("X-Rasid-Time"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return id, at, fmt.Errorf("X-Rasid-Time must be RFC 3339")
		}
		at = t
	}
	return id, at, nil
}

func (s *server) routes(webDir string) http.Handler {
	mux := http.NewServeMux()
	if webDir != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(webDir)))
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "mode": "devnet"})
	})
	mux.HandleFunc("POST /tx/{contract}/{function}", s.handleTx)
	mux.HandleFunc("GET /query/{contract}/{function}", s.handleQuery)
	mux.HandleFunc("POST /bids/{receivableId}", s.handleBid)
	mux.HandleFunc("GET /events", s.handleEvents)
	s.auth.routes(mux)
	return mux
}

var errUnauthorized = errors.New("sign in first")

func identifyError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, errUnauthorized) {
		status = http.StatusUnauthorized
	}
	writeError(w, status, err)
}

func (s *server) handleTx(w http.ResponseWriter, r *http.Request) {
	id, at, err := s.identify(r)
	if err != nil {
		identifyError(w, err)
		return
	}
	var req struct {
		Args      []any                      `json:"args"`
		Transient map[string]json.RawMessage `json:"transient"`
	}
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
	res, err := s.invoke(id, at, r.PathValue("contract")+":"+r.PathValue("function"), args, transient, true)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleQuery(w http.ResponseWriter, r *http.Request) {
	id, at, err := s.identify(r)
	if err != nil {
		identifyError(w, err)
		return
	}
	res, err := s.invoke(id, at, r.PathValue("contract")+":"+r.PathValue("function"), r.URL.Query()["arg"], nil, false)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(res.Result)
}

func (s *server) handleBid(w http.ResponseWriter, r *http.Request) {
	id, at, err := s.identify(r)
	if err != nil {
		identifyError(w, err)
		return
	}
	var req struct {
		Units   int64 `json:"units"`
		RateBps int64 `json:"rateBps"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	bid := map[string]any{"units": req.Units, "rateBps": req.RateBps, "salt": hex.EncodeToString(salt)}
	raw, _ := json.Marshal(bid)
	res, err := s.invoke(id, at, "Marketplace:SubmitBid", []string{r.PathValue("receivableId")}, map[string][]byte{"bid": raw}, true)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	bid["bidId"] = res.TxID
	writeJSON(w, http.StatusOK, map[string]any{"reveal": bid, "commitment": res.Result})
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if _, _, err := s.identify(r); err != nil {
		identifyError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	c := s.events.subscribe()
	defer s.events.unsubscribe(c)
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
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

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
		default:
		}
	}
}

func (h *hub) subscribe() chan []byte {
	c := make(chan []byte, 256)
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

func main() {
	listen := flag.String("listen", ":8080", "address to serve on")
	web := flag.String("web", "../web", "directory with the web console (empty to disable)")
	users := flag.String("users", "", "JSON file of accounts [{username,password,label,identity}] (default: demo accounts)")
	trust := flag.Bool("trust-headers", false, "also accept X-Rasid-Identity without a login (scripts and tests only)")
	riskURL := flag.String("risk-url", "", "AI risk service that scores each new invoice, e.g. http://127.0.0.1:8090")
	flag.Parse()
	s, err := newServer()
	if err != nil {
		log.Fatalf("load chaincode: %v", err)
	}
	s.trustHeaders = *trust
	if *users != "" {
		accts, err := loadAccounts(*users)
		if err != nil {
			log.Fatalf("users: %v", err)
		}
		s.auth = newAuth(accts, false)
	} else {
		log.Printf("demo accounts npci, risk, msme, buyer, citi, nbfc; password %q", DemoPassword)
	}
	if *riskURL != "" {
		if resp, err := http.Get(*riskURL + "/health"); err == nil {
			resp.Body.Close()
			log.Printf("AI risk scoring on: %s scores each new invoice", *riskURL)
		} else {
			log.Printf("AI risk service not reachable at %s yet; new invoices will be scored once it is up", *riskURL)
		}
		go s.runRiskBridge(*riskURL)
	}
	log.Printf("RASID devnet (in-memory ledger, real chaincode) on %s", *listen)
	srv := &http.Server{Addr: *listen, Handler: s.routes(*web), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
