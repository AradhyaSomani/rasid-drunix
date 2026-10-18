package contracts

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EInvoiceData is the invoice summary inside the IRP signed QR code.
// Field names follow the GST e-invoice signed QR "data" payload.
type EInvoiceData struct {
	SellerGstin string  `json:"SellerGstin"`
	BuyerGstin  string  `json:"BuyerGstin"`
	DocNo       string  `json:"DocNo"`
	DocTyp      string  `json:"DocTyp"`
	DocDt       string  `json:"DocDt"` // dd/mm/yyyy
	TotInvVal   float64 `json:"TotInvVal"`
	ItemCnt     int     `json:"ItemCnt"`
	MainHsnCode string  `json:"MainHsnCode"`
	Irn         string  `json:"Irn"`
	IrnDt       string  `json:"IrnDt"`
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

// signedQRHeaderKid returns the key ID from the JWT header ("default" if absent).
func signedQRHeaderKid(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("signed QR is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("bad JWT header encoding: %w", err)
	}
	var h jwtHeader
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", fmt.Errorf("bad JWT header: %w", err)
	}
	if h.Alg != "RS256" {
		return "", fmt.Errorf("unsupported JWT alg %q, IRP signs with RS256", h.Alg)
	}
	if h.Kid == "" {
		return "default", nil
	}
	return h.Kid, nil
}

// VerifySignedQR checks the RS256 signature of an IRP signed QR JWT against
// the IRP public key and returns the invoice data. It is pure and
// deterministic, so every endorsing peer reaches the same verdict without an oracle.
func VerifySignedQR(token string, pub *rsa.PublicKey) (*EInvoiceData, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("signed QR is not a JWT")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("bad JWT signature encoding: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("IRP signature invalid: %w", err)
	}
	rawPayload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("bad JWT payload encoding: %w", err)
	}
	// The IRP payload wraps the invoice JSON as a string in "data".
	var payload struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(rawPayload, &payload); err != nil {
		return nil, fmt.Errorf("bad JWT payload: %w", err)
	}
	var d EInvoiceData
	if err := json.Unmarshal([]byte(payload.Data), &d); err != nil {
		return nil, fmt.Errorf("bad e-invoice data: %w", err)
	}
	if d.Irn == "" || d.SellerGstin == "" || d.BuyerGstin == "" || d.DocNo == "" {
		return nil, fmt.Errorf("e-invoice data missing IRN, GSTINs or document number")
	}
	return &d, nil
}

// ParseRSAPublicKeyPEM accepts a PKIX "PUBLIC KEY" or an X.509 certificate.
func ParseRSAPublicKeyPEM(pemStr string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	var key any
	var err error
	switch block.Type {
	case "CERTIFICATE":
		var cert *x509.Certificate
		cert, err = x509.ParseCertificate(block.Bytes)
		if err == nil {
			key = cert.PublicKey
		}
	default:
		key, err = x509.ParsePKIXPublicKey(block.Bytes)
	}
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("IRP key is not RSA")
	}
	return rsaKey, nil
}

// DocDate converts the e-invoice dd/mm/yyyy date to YYYY-MM-DD.
func (d *EInvoiceData) DocDate() (string, error) {
	t, err := time.Parse("02/01/2006", d.DocDt)
	if err != nil {
		return "", fmt.Errorf("e-invoice DocDt %q is not dd/mm/yyyy", d.DocDt)
	}
	return t.Format(dateLayout), nil
}

// AmountPaise converts TotInvVal (rupees) to paise without float drift.
func (d *EInvoiceData) AmountPaise() int64 {
	s := strconv.FormatFloat(d.TotInvVal, 'f', 2, 64)
	s = strings.Replace(s, ".", "", 1)
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// FinancialYear returns the Indian FY label, e.g. 2026-10-17 -> "2026-27".
func FinancialYear(date string) (string, error) {
	t, err := parseDate(date)
	if err != nil {
		return "", err
	}
	start := t.Year()
	if t.Month() < time.April {
		start--
	}
	return fmt.Sprintf("%d-%02d", start, (start+1)%100), nil
}

// ReceivableIDFor is the network-wide uniqueness key of an invoice:
// SHA-256 of supplier GSTIN, document number and financial year. Both
// e-invoice and manual registrations use it, so the same invoice cannot be
// registered once through each route.
func ReceivableIDFor(sellerGSTIN, docNo, fy string) string {
	return sha256Hex(strings.ToUpper(strings.TrimSpace(sellerGSTIN)) + "|" +
		strings.ToUpper(strings.TrimSpace(docNo)) + "|" + fy)
}
