package main

import (
	"crypto/rand"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Each gateway signs with one organisation's certificate, so everyone who
// signs in here acts as that identity. With LOGIN_USERS set, the transaction,
// query, bid and event endpoints need a session from POST /auth/login. In
// production each bank's own single sign-on would sit here instead.

// Identity is what this gateway's certificate lets its users do on chain.
type Identity struct {
	MSP   string `json:"msp"`
	Role  string `json:"role"`
	GSTIN string `json:"gstin,omitempty"`
}

type account struct {
	Username string   `json:"username"`
	Label    string   `json:"label"`
	Identity Identity `json:"identity"`
}

type auth struct {
	users    map[string]string // username -> password
	ident    Identity
	label    string
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
	user     map[string]string    // token -> username
}

const sessionTTL = 12 * time.Hour

// parseUsers reads LOGIN_USERS, "name:password[,name:password...]".
func parseUsers(spec string) (map[string]string, error) {
	users := map[string]string{}
	for _, pair := range strings.Split(spec, ",") {
		name, pass, ok := strings.Cut(strings.TrimSpace(pair), ":")
		if !ok || name == "" || pass == "" {
			return nil, fmt.Errorf("LOGIN_USERS entry %q is not name:password", pair)
		}
		users[strings.ToLower(name)] = pass
	}
	return users, nil
}

// fabricAttrOID is the certificate extension Fabric CA uses for attributes.
var fabricAttrOID = asn1.ObjectIdentifier{1, 2, 3, 4, 5, 6, 7, 8, 1}

// certIdentity reads the RASID role and GSTIN from the enrollment certificate.
func certIdentity(certPath, msp string) (Identity, error) {
	b, err := os.ReadFile(certPath)
	if err != nil {
		return Identity{}, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return Identity{}, fmt.Errorf("%s is not PEM", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Identity{}, err
	}
	id := Identity{MSP: msp}
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(fabricAttrOID) {
			continue
		}
		var attrs struct {
			Attrs map[string]string `json:"attrs"`
		}
		if err := json.Unmarshal(ext.Value, &attrs); err != nil {
			return id, err
		}
		id.Role, id.GSTIN = attrs.Attrs["rasid.role"], attrs.Attrs["rasid.gstin"]
	}
	return id, nil
}

func newAuth(users map[string]string, ident Identity, label string) *auth {
	if label == "" {
		label = ident.MSP
	}
	return &auth{users: users, ident: ident, label: label, sessions: map[string]time.Time{}, user: map[string]string{}}
}

var errBadLogin = errors.New("wrong username or password")

func (a *auth) login(username, password string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	want, ok := a.users[name]
	if !ok || subtle.ConstantTimeCompare([]byte(want), []byte(password)) != 1 {
		return "", errBadLogin
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	a.mu.Lock()
	a.sessions[token], a.user[token] = time.Now().Add(sessionTTL), name
	a.mu.Unlock()
	return token, nil
}

func (a *auth) lookup(token string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[token]
	if !ok || time.Now().After(exp) {
		delete(a.sessions, token)
		delete(a.user, token)
		return "", false
	}
	return a.user[token], true
}

func (a *auth) logout(token string) {
	a.mu.Lock()
	delete(a.sessions, token)
	delete(a.user, token)
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

func (a *auth) account(name string) account { return account{name, a.label, a.ident} }

// require lets a request through only with a live session.
func (a *auth) require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.lookup(bearerToken(r)); !ok {
			writeError(w, http.StatusUnauthorized, errors.New("sign in first"))
			return
		}
		next(w, r)
	}
}

func (a *auth) routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		token, err := a.login(req.Username, req.Password)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": token, "user": a.account(strings.ToLower(req.Username))})
	})
	mux.HandleFunc("GET /auth/me", func(w http.ResponseWriter, r *http.Request) {
		name, ok := a.lookup(bearerToken(r))
		if !ok {
			writeError(w, http.StatusUnauthorized, errors.New("not signed in"))
			return
		}
		writeJSON(w, http.StatusOK, a.account(name))
	})
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		a.logout(bearerToken(r))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /auth/accounts", func(w http.ResponseWriter, _ *http.Request) {
		list := []account{}
		for name := range a.users {
			list = append(list, a.account(name))
		}
		writeJSON(w, http.StatusOK, map[string]any{"accounts": list})
	})
}
