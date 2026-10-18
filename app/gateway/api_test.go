package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStringArgs(t *testing.T) {
	got, err := stringArgs([]any{"abc", float64(6000), []any{"OK", "CIRCULAR_TRADE"}, float64(100000000)})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"abc", "6000", `["OK","CIRCULAR_TRADE"]`, "100000000"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAsJSON(t *testing.T) {
	if string(asJSON([]byte(`{"a":1}`))) != `{"a":1}` {
		t.Fatal("valid JSON should pass through")
	}
	if string(asJSON([]byte("true-ish"))) != `"true-ish"` {
		t.Fatal("plain text should be quoted")
	}
	if asJSON(nil) != nil {
		t.Fatal("empty result should be nil")
	}
}

func TestHubDropsForSlowClients(t *testing.T) {
	h := newHub()
	c := h.subscribe()
	for i := 0; i < 100; i++ {
		h.publish([]byte("x")) // must never block
	}
	if len(c) != cap(c) {
		t.Fatalf("buffer = %d", len(c))
	}
	h.unsubscribe(c)
}

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rec := httptest.NewRecorder()
	withCORS("*", ok).ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/tx/a/b", nil))
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	withCORS("", ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusTeapot || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("CORS should be off by default")
	}
}

func TestLoginGuardsEndpoints(t *testing.T) {
	users, err := parseUsers("citi:pw, Ops:pw2")
	if err != nil || users["ops"] != "pw2" {
		t.Fatalf("parseUsers: %v %v", users, err)
	}
	if _, err := parseUsers("nopassword"); err == nil {
		t.Fatal("entry without a password should be rejected")
	}
	app := &App{events: newHub(), auth: newAuth(users, Identity{MSP: "CitiMSP", Role: "financier"}, "Citi")}
	h := app.routes()
	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	for _, p := range []string{"/query/InvoiceRegistry/GetReceivable", "/events"} {
		if rec := do("GET", p, "", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without login: %d", p, rec.Code)
		}
	}
	if rec := do("POST", "/tx/InvoiceRegistry/InitLedger", "", `{"args":[]}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tx without login: %d", rec.Code)
	}
	if rec := do("POST", "/auth/login", "", `{"username":"citi","password":"nope"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", rec.Code)
	}
	rec := do("POST", "/auth/login", "", `{"username":"CITI","password":"pw"}`)
	var out struct {
		Token string  `json:"token"`
		User  account `json:"user"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.User.Identity.Role != "financier" {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/auth/me", out.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("me: %d", rec.Code)
	}
	do("POST", "/auth/logout", out.Token, "")
	if rec := do("GET", "/auth/me", out.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token survived logout: %d", rec.Code)
	}
}

func TestCertIdentity(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour),
		ExtraExtensions: []pkix.Extension{{Id: fabricAttrOID, Value: []byte(`{"attrs":{"rasid.role":"supplier","rasid.gstin":"27AAACM1234A1Z5"}}`)}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "cert.pem")
	_ = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	id, err := certIdentity(path, "BankAMSP")
	if err != nil || id != (Identity{MSP: "BankAMSP", Role: "supplier", GSTIN: "27AAACM1234A1Z5"}) {
		t.Fatalf("certIdentity = %+v, %v", id, err)
	}
}
