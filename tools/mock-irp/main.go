// Command mock-irp stands in for the GST Invoice Registration Portal during
// the hackathon. It creates an RSA signing key and issues signed QR JWTs in
// the same shape as the IRP, so the RASID chaincode can verify them.
//
//	mock-irp keygen -out keys/            writes irp-private.pem and irp-public.pem
//	mock-irp sign -key keys/irp-private.pem -invoice invoice.json [-kid irp-2026]
//
// invoice.json holds the signed QR fields: SellerGstin, BuyerGstin, DocNo,
// DocTyp, DocDt (dd/mm/yyyy), TotInvVal, ItemCnt, MainHsnCode. DocTyp, DocDt
// (today), Irn and IrnDt are filled in if missing. The JWT is printed to stdout.
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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		fs := flag.NewFlagSet("keygen", flag.ExitOnError)
		out := fs.String("out", ".", "directory for the key files")
		fs.Parse(os.Args[2:])
		err = keygen(*out)
	case "sign":
		fs := flag.NewFlagSet("sign", flag.ExitOnError)
		keyPath := fs.String("key", "irp-private.pem", "IRP private key")
		invPath := fs.String("invoice", "", "invoice JSON file")
		kid := fs.String("kid", "irp-2026", "key ID put in the JWT header")
		fs.Parse(os.Args[2:])
		var jwt string
		jwt, err = sign(*keyPath, *invPath, *kid)
		if err == nil {
			fmt.Println(jwt)
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mock-irp:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: mock-irp keygen -out DIR | mock-irp sign -key FILE -invoice FILE [-kid ID]")
	os.Exit(2)
}

func keygen(dir string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return err
	}
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, "irp-private.pem"), priv, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "irp-public.pem"), pub, 0o644)
}

func sign(keyPath, invPath, kid string) (string, error) {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return "", fmt.Errorf("%s is not PEM", keyPath)
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(invPath)
	if err != nil {
		return "", err
	}
	var inv map[string]any
	if err := json.Unmarshal(raw, &inv); err != nil {
		return "", fmt.Errorf("invoice JSON: %w", err)
	}
	for _, f := range []string{"SellerGstin", "BuyerGstin", "DocNo", "TotInvVal"} {
		if _, ok := inv[f]; !ok {
			return "", fmt.Errorf("invoice is missing %s", f)
		}
	}
	if _, ok := inv["DocTyp"]; !ok {
		inv["DocTyp"] = "INV"
	}
	if _, ok := inv["DocDt"]; !ok {
		inv["DocDt"] = time.Now().Format("02/01/2006")
	}
	if _, ok := inv["Irn"]; !ok {
		// The real IRN is a SHA-256 over GSTIN, FY, document type and number.
		h := sha256.Sum256([]byte(fmt.Sprintf("%v|%v|%v", inv["SellerGstin"], inv["DocTyp"], inv["DocNo"])))
		inv["Irn"] = hex.EncodeToString(h[:])
	}
	if _, ok := inv["IrnDt"]; !ok {
		inv["IrnDt"] = time.Now().Format("2006-01-02 15:04:05")
	}
	data, _ := json.Marshal(inv)
	payload, _ := json.Marshal(map[string]string{"data": string(data), "iss": "NIC"})
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return strings.Join([]string{signing, enc.EncodeToString(sig)}, "."), nil
}
