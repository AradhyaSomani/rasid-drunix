package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// demoIRP is a mock GST IRP built into the devnet, so a live demo can create
// signed e-invoices from the console instead of a terminal. It signs exactly
// as tools/mock-irp does, and the chaincode verifies the signature the same
// way. Turned on with -demo-irp; never on a live network.
type demoIRP struct {
	kid    string
	key    *rsa.PrivateKey
	pubPEM string
}

func newDemoIRP() (*demoIRP, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &demoIRP{kid: "irp-2026", key: key, pubPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}, nil
}

// sign returns the signed QR JWT for one invoice.
func (d *demoIRP) sign(inv map[string]any) (string, error) {
	h := sha256.Sum256([]byte(fmt.Sprintf("%v|%v|%v", inv["SellerGstin"], inv["DocTyp"], inv["DocNo"])))
	inv["Irn"] = hex.EncodeToString(h[:])
	inv["IrnDt"] = time.Now().Format("2006-01-02 15:04:05")
	data, _ := json.Marshal(inv)
	payload, _ := json.Marshal(map[string]string{"data": string(data), "iss": "NIC"})
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": d.kid, "typ": "JWT"})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, d.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

func (s *server) demoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /demo/irp-key", func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := s.identify(r); err != nil {
			identifyError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"kid": s.irp.kid, "pem": s.irp.pubPEM})
	})
	// POST /demo/invoice signs an e-invoice raised by the signed-in supplier.
	mux.HandleFunc("POST /demo/invoice", func(w http.ResponseWriter, r *http.Request) {
		id, _, err := s.identify(r)
		if err != nil {
			identifyError(w, err)
			return
		}
		if id.Role != "supplier" || id.GSTIN == "" {
			writeError(w, http.StatusForbidden, errors.New("only a supplier can raise an e-invoice"))
			return
		}
		var req struct {
			BuyerGSTIN string  `json:"buyerGstin"`
			Rupees     float64 `json:"rupees"`
			DocNo      string  `json:"docNo"`
		}
		if err := decodeBody(r, &req); err != nil {
			bodyError(w, err)
			return
		}
		if req.BuyerGSTIN == "" {
			req.BuyerGSTIN = "29AABCB5678B1Z2" // the demo buyer
		}
		if req.Rupees <= 0 {
			req.Rupees = 1000000
		}
		if req.Rupees > 1e10 {
			writeError(w, http.StatusBadRequest, errors.New("amount is over ₹1,000 crore"))
			return
		}
		if req.DocNo == "" {
			n, _ := rand.Int(rand.Reader, big.NewInt(9000))
			req.DocNo = fmt.Sprintf("MSME/26-27/%04d", n.Int64()+1000)
		}
		if len(req.DocNo) > 16 || strings.ContainsAny(req.DocNo, "<>\"'") {
			writeError(w, http.StatusBadRequest, errors.New("document number must be at most 16 plain characters"))
			return
		}
		inv := map[string]any{
			"SellerGstin": id.GSTIN, "BuyerGstin": strings.ToUpper(strings.TrimSpace(req.BuyerGSTIN)), "DocNo": req.DocNo,
			"DocTyp": "INV", "DocDt": time.Now().Format("02/01/2006"), "TotInvVal": req.Rupees, "ItemCnt": 3, "MainHsnCode": "8481",
		}
		qr, err := s.irp.sign(inv)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"qr": qr, "invoice": inv})
	})
}

// decodeBody reads a JSON body; an empty body leaves v as it is.
func decodeBody(r *http.Request, v any) error {
	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil && err.Error() == "EOF" {
		return nil
	}
	return err
}
