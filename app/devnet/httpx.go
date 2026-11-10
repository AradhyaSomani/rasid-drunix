package main

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// maxBody caps every request body. The largest real request, a signed
// e-invoice QR, is a few kilobytes.
const maxBody = 1 << 20

// contentSecurityPolicy lets the console load only its own files and talk
// only to this server. Inline style attributes are allowed for the bar
// widths; scripts must come from app.js.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// harden wraps every handler: security headers, a body size cap, and one log
// line per request (never the Authorization header or the token).
func harden(next http.Handler, quiet bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rec, r)
		if !quiet && r.URL.Path != "/events" && r.URL.Path != "/health" {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) { s.status = code; s.ResponseWriter.WriteHeader(code) }

// Flush keeps the event stream working through the recorder.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// bodyError is a 413 for an oversized body and a 400 for anything else.
func bodyError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("request body is over 1 MB"))
		return
	}
	writeError(w, http.StatusBadRequest, errors.New("request body is not valid JSON: "+err.Error()))
}

// chaincodeStatus maps a chaincode refusal to an HTTP status, so clients can
// tell "not allowed" (403) from "not found" (404) and "wrong state or
// duplicate" (409). Anything else is a bad request (400).
func chaincodeStatus(msg string) int {
	m := strings.ToLower(msg)
	has := func(parts ...string) bool {
		for _, p := range parts {
			if strings.Contains(m, p) {
				return true
			}
		}
		return false
	}
	switch {
	case has("may not call", "not an authorised", "caller is not", "cannot be withdrawn by", "must be called by",
		"has no rasid.", "unknown role", "identity needs"):
		return http.StatusForbidden
	case has("does not exist", "not found"):
		return http.StatusNotFound
	case has("duplicate", "already", "cannot move from", "expected one of", "exceeds listing limit", "no ai risk score",
		"is not past", "days past due", "mvcc", "phantom_read", "is already due", "no longer be financed", "awarded twice", "have until"):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}
