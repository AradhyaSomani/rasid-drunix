// Command issue-identity signs a client certificate with an organisation's CA
// key and embeds RASID attributes the way Fabric CA does (extension
// 1.2.3.4.5.6.7.8.1), so a local network built with cryptogen can carry
// rasid.role and rasid.gstin without running Fabric CA.
//
//	issue-identity -ca crypto/peerOrganizations/citi.rasid/ca -name citi-financier \
//	  -attr rasid.role=financier -out ids/citi
//
// Writes cert.pem and key.pem. For production, register users in each org's
// Fabric CA with the same attributes instead.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var attrOID = asn1.ObjectIdentifier{1, 2, 3, 4, 5, 6, 7, 8, 1}

type attrList map[string]string

func (a attrList) String() string { return fmt.Sprint(map[string]string(a)) }
func (a attrList) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return errors.New("attribute must be name=value")
	}
	a[k] = val
	return nil
}

func main() {
	caDir := flag.String("ca", "", "cryptogen CA folder holding the CA certificate and priv_sk")
	name := flag.String("name", "", "common name of the new identity")
	out := flag.String("out", "", "folder to write cert.pem and key.pem")
	attrs := attrList{}
	flag.Var(attrs, "attr", "attribute name=value (repeatable)")
	flag.Parse()
	if *caDir == "" || *name == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := issue(*caDir, *name, *out, attrs); err != nil {
		fmt.Fprintln(os.Stderr, "issue-identity:", err)
		os.Exit(1)
	}
}

func issue(caDir, name, out string, attrs attrList) error {
	caCert, caKey, err := loadCA(caDir)
	if err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	attrJSON, err := json.Marshal(map[string]any{"attrs": attrs})
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	// Fabric checks the CA chain as of the new certificate's NotBefore, so it
	// must not start before the CA does (cryptogen rounds the CA's to the minute).
	notBefore := time.Now().Add(-5 * time.Minute)
	if notBefore.Before(caCert.NotBefore) {
		notBefore = caCert.NotBefore
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: name, OrganizationalUnit: []string{"client"},
			Country: caCert.Subject.Country, Province: caCert.Subject.Province, Locality: caCert.Subject.Locality,
		},
		NotBefore:             notBefore,
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		ExtraExtensions:       []pkix.Extension{{Id: attrOID, Value: attrJSON}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
}

func loadCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certs, _ := filepath.Glob(filepath.Join(dir, "*-cert.pem"))
	if len(certs) != 1 {
		return nil, nil, fmt.Errorf("expected one *-cert.pem in %s, found %d", dir, len(certs))
	}
	certPEM, err := os.ReadFile(certs[0])
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("%s is not PEM", certs[0])
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, "priv_sk"))
	if err != nil {
		return nil, nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("priv_sk is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	ecKey, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, errors.New("CA key is not ECDSA")
	}
	return cert, ecKey, nil
}
