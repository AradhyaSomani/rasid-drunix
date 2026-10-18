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
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"google.golang.org/protobuf/proto"
)

// Identity is who a devnet request acts as, sent in the X-Rasid-Identity header:
// {"msp":"CitiMSP","role":"financier"} or {"msp":"SupplierBankMSP","role":"supplier","gstin":"27AAACM1234A1Z5"}
type Identity struct {
	MSP   string `json:"msp"`
	Role  string `json:"role"`
	GSTIN string `json:"gstin,omitempty"`
}

// fabricAttrOID is the certificate extension Fabric CA uses for attributes.
var fabricAttrOID = asn1.ObjectIdentifier{1, 2, 3, 4, 5, 6, 7, 8, 1}

var (
	creatorMu    sync.Mutex
	creatorCache = map[Identity][]byte{}
)

// creatorFor builds the serialized identity a peer would see: an X.509
// certificate carrying rasid.role / rasid.gstin exactly as Fabric CA encodes
// attributes, so the chaincode's cid checks run unchanged.
func creatorFor(id Identity) ([]byte, error) {
	if id.MSP == "" || id.Role == "" {
		return nil, fmt.Errorf("identity needs msp and role")
	}
	creatorMu.Lock()
	defer creatorMu.Unlock()
	if b, ok := creatorCache[id]; ok {
		return b, nil
	}
	attrs := map[string]string{"rasid.role": id.Role}
	if id.GSTIN != "" {
		attrs["rasid.gstin"] = id.GSTIN
	}
	attrJSON, _ := json.Marshal(map[string]any{"attrs": attrs})
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:    big.NewInt(time.Now().UnixNano()),
		Subject:         pkix.Name{CommonName: id.Role + "@" + id.MSP, OrganizationalUnit: []string{"client"}},
		NotBefore:       time.Now().Add(-time.Hour),
		NotAfter:        time.Now().Add(365 * 24 * time.Hour),
		ExtraExtensions: []pkix.Extension{{Id: fabricAttrOID, Value: attrJSON}},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	b, err := proto.Marshal(&msp.SerializedIdentity{Mspid: id.MSP, IdBytes: certPEM})
	if err != nil {
		return nil, err
	}
	creatorCache[id] = b
	return b, nil
}
