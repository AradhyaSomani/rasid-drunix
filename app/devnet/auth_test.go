package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestLoginDecidesIdentity checks that, without -trust-headers, only a
// session acts on the ledger and the session's account decides the identity.
func TestLoginDecidesIdentity(t *testing.T) {
	s, err := newServer()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.routes(""))
	defer ts.Close()

	call := func(method, path, token string, header map[string]string, body any) (int, map[string]any) {
		t.Helper()
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	login := func(user, pass string) (int, string) {
		code, out := call("POST", "/auth/login", "", nil, map[string]string{"username": user, "password": pass})
		tok, _ := out["token"].(string)
		return code, tok
	}
	initLedger := map[string]any{"args": []any{"NPCIMSP", []string{"NPCIMSP"}}}
	operatorHeader := map[string]string{"X-Rasid-Identity": `{"msp":"NPCIMSP","role":"operator"}`}

	if code, _ := call("POST", "/tx/InvoiceRegistry/InitLedger", "", nil, initLedger); code != http.StatusUnauthorized {
		t.Fatalf("no login: %d, want 401", code)
	}
	if code, _ := call("POST", "/tx/InvoiceRegistry/InitLedger", "", operatorHeader, initLedger); code != http.StatusUnauthorized {
		t.Fatalf("identity header without -trust-headers: %d, want 401", code)
	}
	if code, _ := call("GET", "/events", "", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("events without login: %d, want 401", code)
	}
	if code, _ := login("msme", "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d, want 401", code)
	}

	code, msme := login("msme", DemoPassword)
	if code != http.StatusOK || msme == "" {
		t.Fatalf("msme login: %d", code)
	}
	if _, me := call("GET", "/auth/me", msme, nil, nil); me["identity"].(map[string]any)["role"] != "supplier" {
		t.Fatalf("me = %v", me)
	}
	// A signed-in MSME claiming the operator role in a header still acts as the MSME.
	code, out := call("POST", "/tx/InvoiceRegistry/InitLedger", msme, operatorHeader, initLedger)
	if code == http.StatusOK || !strings.Contains(out["error"].(string), "operator") {
		t.Fatalf("msme ran InitLedger: %d %v", code, out)
	}

	_, npci := login("NPCI", DemoPassword) // usernames are case-insensitive
	if code, out := call("POST", "/tx/InvoiceRegistry/InitLedger", npci, nil, initLedger); code != http.StatusOK {
		t.Fatalf("operator InitLedger: %d %v", code, out)
	}
	if code, _ := call("POST", "/auth/logout", npci, nil, nil); code != http.StatusNoContent {
		t.Fatalf("logout: %d", code)
	}
	if code, _ := call("GET", "/auth/me", npci, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("token still valid after logout: %d", code)
	}
	if _, acc := call("GET", "/auth/accounts", "", nil, nil); len(acc["accounts"].([]any)) != 6 || acc["demoPassword"] != DemoPassword {
		t.Fatalf("accounts = %v", acc)
	}
}
