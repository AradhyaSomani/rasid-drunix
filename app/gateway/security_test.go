package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestGatewayHardening: headers, the body cap and sign-in on every ledger
// endpoint, without a peer behind the gateway.
func TestGatewayHardening(t *testing.T) {
	app := &App{cfg: config{MSPID: "CitiMSP", ConnectSrc: "'self'"}, events: newHub()}
	app.auth = newAuth(map[string]string{"citi": "pw"}, Identity{MSP: "CitiMSP", Role: "financier"}, "Citi")
	ts := httptest.NewServer(app.routes())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer"} {
		if resp.Header.Get(k) != v {
			t.Errorf("%s = %q", k, resp.Header.Get(k))
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self';") || !strings.Contains(csp, "connect-src 'self';") {
		t.Errorf("CSP = %q", csp)
	}
	for _, ep := range []struct{ method, path string }{
		{"POST", "/tx/InvoiceRegistry/InitLedger"}, {"GET", "/query/InvoiceRegistry/ListReceivables"}, {"POST", "/bids/x"}, {"GET", "/events"},
	} {
		req, _ := http.NewRequest(ep.method, ts.URL+ep.path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer forged")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: %d, want 401", ep.method, ep.path, resp.StatusCode)
		}
	}
	big := strings.NewReader(`{"username":"` + strings.Repeat("a", 2<<20) + `"}`)
	if resp, _ := http.Post(ts.URL+"/auth/login", "application/json", big); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("2 MB login: %d, want 413", resp.StatusCode)
	}
}

func TestGatewayLoginThrottle(t *testing.T) {
	a := newAuth(map[string]string{"citi": "pw"}, Identity{MSP: "CitiMSP", Role: "financier"}, "Citi")
	a.lockout = 100 * time.Millisecond
	mux := http.NewServeMux()
	a.routes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	login := func(pass string) int {
		b, _ := json.Marshal(map[string]string{"username": "citi", "password": pass})
		resp, err := http.Post(ts.URL+"/auth/login", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < maxFails; i++ {
		login("guess")
	}
	if code := login("pw"); code != http.StatusTooManyRequests {
		t.Fatalf("locked login: %d, want 429", code)
	}
	time.Sleep(150 * time.Millisecond)
	if code := login("pw"); code != http.StatusOK {
		t.Fatalf("after lockout: %d", code)
	}
}

func TestLedgerErrorStatus(t *testing.T) {
	for err, want := range map[error]int{
		errors.New("dial tcp 127.0.0.1:7051: connection refused"):                       502,
		errors.New("transaction abc failed to commit: MVCC_READ_CONFLICT"):              409,
		errors.New("chaincode response 500, receivable x does not exist"):               404,
		errors.New("chaincode response 500, role buyer may not call this function (x)"): 403,
	} {
		rec := httptest.NewRecorder()
		ledgerError(rec, err)
		if rec.Code != want {
			t.Errorf("%v: %d, want %d", err, rec.Code, want)
		}
	}
}
