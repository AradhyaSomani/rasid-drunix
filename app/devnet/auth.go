package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Account is a console login. Each one acts as exactly one ledger identity,
// so what a user can do is decided by the server, not by the browser.
type Account struct {
	Username string   `json:"username"`
	Password string   `json:"password,omitempty"`
	Label    string   `json:"label"`
	Identity Identity `json:"identity"`
}

// DemoPassword is the shared password of the built-in demo accounts.
const DemoPassword = "rasid"

func demoAccounts(password string) []Account {
	return []Account{
		{"npci", password, "NPCI operator", Identity{MSP: "NPCIMSP", Role: "operator"}},
		{"risk", password, "AI risk desk", Identity{MSP: "NPCIMSP", Role: "risk_oracle"}},
		{"msme", password, "MSME supplier", Identity{MSP: "SupplierBankMSP", Role: "supplier", GSTIN: "27AAACM1234A1Z5"}},
		{"buyer", password, "Corporate buyer", Identity{MSP: "BuyerBankMSP", Role: "buyer", GSTIN: "29AABCB5678B1Z2"}},
		{"citi", password, "Citi", Identity{MSP: "CitiMSP", Role: "financier"}},
		{"nbfc", password, "NBFC", Identity{MSP: "NbfcMSP", Role: "financier"}},
	}
}

func loadAccounts(path string) ([]Account, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var accts []Account
	if err := json.Unmarshal(b, &accts); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return accts, nil
}

const sessionTTL = 12 * time.Hour

type session struct {
	acct    Account
	expires time.Time
}

// auth keeps logins in memory; a restart signs everyone out, like the ledger.
type auth struct {
	accounts map[string]Account
	demo     bool // built-in accounts with the published demo password
	mu       sync.Mutex
	sessions map[string]session
}

func newAuth(accts []Account, demo bool) *auth {
	a := &auth{accounts: map[string]Account{}, demo: demo, sessions: map[string]session{}}
	for _, acct := range accts {
		a.accounts[strings.ToLower(acct.Username)] = acct
	}
	return a
}

var errBadLogin = errors.New("wrong username or password")

func (a *auth) login(username, password string) (string, Account, error) {
	acct, ok := a.accounts[strings.ToLower(strings.TrimSpace(username))]
	if !ok || subtle.ConstantTimeCompare([]byte(acct.Password), []byte(password)) != 1 {
		return "", Account{}, errBadLogin
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", Account{}, err
	}
	token := hex.EncodeToString(b)
	a.mu.Lock()
	a.sessions[token] = session{acct: acct, expires: time.Now().Add(sessionTTL)}
	a.mu.Unlock()
	return token, acct, nil
}

func (a *auth) lookup(token string) (Account, bool) {
	if token == "" {
		return Account{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[token]
	if !ok || time.Now().After(s.expires) {
		delete(a.sessions, token)
		return Account{}, false
	}
	return s.acct, true
}

func (a *auth) logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	a.mu.Unlock()
}

// bearerToken reads "Authorization: Bearer <token>", or ?token= for
// EventSource, which cannot set headers.
func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

type publicAccount struct {
	Username string   `json:"username"`
	Label    string   `json:"label"`
	Identity Identity `json:"identity"`
}

func public(a Account) publicAccount { return publicAccount{a.Username, a.Label, a.Identity} }

func (a *auth) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		token, acct, err := a.login(req.Username, req.Password)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": public(acct)})
	})
	mux.HandleFunc("GET /auth/me", func(w http.ResponseWriter, r *http.Request) {
		acct, ok := a.lookup(bearerToken(r))
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("not signed in"))
			return
		}
		writeJSON(w, http.StatusOK, public(acct))
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		a.logout(bearerToken(r))
		w.WriteHeader(http.StatusNoContent)
	})
	// The login page lists who can sign in; passwords are never returned.
	mux.HandleFunc("GET /auth/accounts", func(w http.ResponseWriter, _ *http.Request) {
		list := []publicAccount{}
		for _, acct := range a.accounts {
			list = append(list, public(acct))
		}
		resp := map[string]any{"accounts": list}
		if a.demo {
			resp["demoPassword"] = DemoPassword
		}
		writeJSON(w, http.StatusOK, resp)
	})
}
