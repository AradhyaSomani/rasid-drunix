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
	fails    map[string]*failures
	lockout  time.Duration // first lockout after maxFails wrong passwords; doubles after each further miss
}

// maxFails wrong passwords in a row lock a username for a while.
const maxFails = 5

type failures struct {
	n     int
	until time.Time
}

// errThrottled means too many wrong passwords; the client should wait.
type errThrottled struct{ wait time.Duration }

func (e errThrottled) Error() string {
	return fmt.Sprintf("too many wrong passwords; try again in %d seconds", int(e.wait.Seconds()+0.999))
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
	return &auth{users: users, ident: ident, label: label, sessions: map[string]time.Time{}, user: map[string]string{},
		fails: map[string]*failures{}, lockout: 30 * time.Second}
}

var errBadLogin = errors.New("wrong username or password")

func (a *auth) login(username, password string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	a.mu.Lock()
	if f := a.fails[name]; f != nil && time.Now().Before(f.until) {
		a.mu.Unlock()
		return "", errThrottled{time.Until(f.until)}
	}
	a.mu.Unlock()
	want, ok := a.users[name]
	if !ok || subtle.ConstantTimeCompare([]byte(want), []byte(password)) != 1 {
		a.failed(name)
		return "", errBadLogin
	}
	a.mu.Lock()
	delete(a.fails, name)
	a.mu.Unlock()
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

func (a *auth) failed(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.fails) > 10000 { // someone is spraying usernames; start over rather than grow without limit
		a.fails = map[string]*failures{}
	}
	f := a.fails[name]
	if f == nil {
		f = &failures{}
		a.fails[name] = f
	}
	f.n++
	if f.n >= maxFails {
		f.until = time.Now().Add(a.lockout << min(f.n-maxFails, 5))
	}
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
			bodyError(w, err)
			return
		}
		token, err := a.login(req.Username, req.Password)
		var slow errThrottled
		if errors.As(err, &slow) {
			w.Header().Set("Retry-After", fmt.Sprint(int(slow.wait.Seconds()+0.999)))
			writeError(w, http.StatusTooManyRequests, err)
			return
		}
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
