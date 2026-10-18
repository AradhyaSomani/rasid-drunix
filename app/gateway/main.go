// Command gateway is the REST front door to the RASID chaincode for one
// organisation's identity. Each org (supplier bank, buyer bank, financier,
// NPCI operator, risk oracle) runs its own gateway with its own certificate.
//
//	POST /tx/{contract}/{function}     {"args":[...], "transient":{"bid":{...}}}   submit
//	GET  /query/{contract}/{function}?arg=..&arg=..                            evaluate
//	POST /bids/{receivableId}          {"units":6000,"rateBps":1000}  sealed bid with a fresh salt
//	GET  /events                       chaincode events as Server-Sent Events
//	GET  /health
//
// With RISK_URL set (risk-oracle org only), it also scores every newly
// registered or accepted receivable through the risk service and anchors the
// result with InvoiceRegistry:RecordRiskScore.
package main

import (
	"context"
	"crypto/x509"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hyperledger/fabric-gateway/pkg/client"
	"github.com/hyperledger/fabric-gateway/pkg/hash"
	"github.com/hyperledger/fabric-gateway/pkg/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type config struct {
	PeerEndpoint  string // host:port of the org's gateway peer
	PeerHostAlias string // TLS server name
	TLSCertPath   string // peer TLS CA
	MSPID         string
	CertPath      string // enrollment certificate (with rasid.role attributes)
	KeyPath       string
	Channel       string
	Chaincode     string
	Listen        string
	RiskURL       string
	OperatorMSP   string // network operator, trusted to endorse sealed bids
	CORSOrigin    string // allowed browser origin for the web console ("*" for a demo)
	WebDir        string // serve the web console from here when set
	LoginUsers    string // "name:password,..." turns on sign-in for the console
	LoginLabel    string // how the console names this gateway's identity, e.g. "Citi"
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func loadConfig() config {
	return config{
		PeerEndpoint:  env("PEER_ENDPOINT", "localhost:7051"),
		PeerHostAlias: env("PEER_HOST_ALIAS", "peer0.npci.rasid"),
		TLSCertPath:   env("PEER_TLS_CA", "crypto/peer-tls-ca.pem"),
		MSPID:         env("MSP_ID", "NPCIMSP"),
		CertPath:      env("CERT_PATH", "crypto/cert.pem"),
		KeyPath:       env("KEY_PATH", "crypto/key.pem"),
		Channel:       env("CHANNEL", "rasid"),
		Chaincode:     env("CHAINCODE", "rasid"),
		Listen:        env("LISTEN", ":8080"),
		RiskURL:       os.Getenv("RISK_URL"),
		OperatorMSP:   env("OPERATOR_MSP", "NPCIMSP"),
		CORSOrigin:    os.Getenv("CORS_ORIGIN"),
		WebDir:        os.Getenv("WEB_DIR"),
		LoginUsers:    os.Getenv("LOGIN_USERS"),
		LoginLabel:    os.Getenv("LOGIN_LABEL"),
	}
}

func main() {
	cfg := loadConfig()
	conn, err := grpcConnection(cfg)
	if err != nil {
		log.Fatalf("connect to peer: %v", err)
	}
	defer conn.Close()

	id, sign, err := loadIdentity(cfg)
	if err != nil {
		log.Fatalf("load identity: %v", err)
	}
	gw, err := client.Connect(id,
		client.WithSign(sign),
		client.WithHash(hash.SHA256),
		client.WithClientConnection(conn),
		client.WithEvaluateTimeout(5*time.Second),
		client.WithEndorseTimeout(15*time.Second),
		client.WithSubmitTimeout(5*time.Second),
		client.WithCommitStatusTimeout(time.Minute),
	)
	if err != nil {
		log.Fatalf("connect gateway: %v", err)
	}
	defer gw.Close()

	network := gw.GetNetwork(cfg.Channel)
	app := &App{
		cfg:      cfg,
		network:  network,
		contract: network.GetContract(cfg.Chaincode),
		events:   newHub(),
	}
	if cfg.LoginUsers != "" {
		users, err := parseUsers(cfg.LoginUsers)
		if err != nil {
			log.Fatal(err)
		}
		ident, err := certIdentity(cfg.CertPath, cfg.MSPID)
		if err != nil {
			log.Fatalf("read identity attributes: %v", err)
		}
		app.auth = newAuth(users, ident, cfg.LoginLabel)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.streamEvents(ctx)
	if cfg.RiskURL != "" {
		go app.runRiskBridge(ctx)
	}

	log.Printf("RASID gateway for %s on %s (channel %s, chaincode %s)", cfg.MSPID, cfg.Listen, cfg.Channel, cfg.Chaincode)
	srv := &http.Server{Addr: cfg.Listen, Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func grpcConnection(cfg config) (*grpc.ClientConn, error) {
	pemBytes, err := os.ReadFile(cfg.TLSCertPath)
	if err != nil {
		return nil, err
	}
	cert, err := identity.CertificateFromPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	creds := credentials.NewClientTLSFromCert(pool, cfg.PeerHostAlias)
	return grpc.NewClient(cfg.PeerEndpoint, grpc.WithTransportCredentials(creds))
}

func loadIdentity(cfg config) (*identity.X509Identity, identity.Sign, error) {
	certPEM, err := os.ReadFile(cfg.CertPath)
	if err != nil {
		return nil, nil, err
	}
	cert, err := identity.CertificateFromPEM(certPEM)
	if err != nil {
		return nil, nil, err
	}
	id, err := identity.NewX509Identity(cfg.MSPID, cert)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, nil, err
	}
	key, err := identity.PrivateKeyFromPEM(keyPEM)
	if err != nil {
		return nil, nil, err
	}
	sign, err := identity.NewPrivateKeySign(key)
	return id, sign, err
}
