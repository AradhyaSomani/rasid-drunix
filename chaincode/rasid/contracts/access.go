package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// Role is read from the "rasid.role" attribute that each organisation's
// Fabric CA puts in the enrollment certificate.
type Role string

const (
	RoleSupplier   Role = "supplier"    // MSME, enrolled by its bank / TReDS platform
	RoleBuyer      Role = "buyer"       // corporate buyer or its bank
	RoleFinancier  Role = "financier"   // bank / NBFC
	RoleRiskOracle Role = "risk_oracle" // AI fraud service signing identity
	RoleOperator   Role = "operator"    // NPCI network operator
)

const (
	attrRole  = "rasid.role"
	attrGSTIN = "rasid.gstin"
)

// Caller is the authenticated identity behind a transaction.
type Caller struct {
	MSP     string
	Role    Role
	PartyID string // hashed GSTIN for suppliers/buyers, "FIN:<msp>" for financiers
}

// PartyIDForGSTIN hashes a GSTIN so that raw GSTINs never sit on the shared ledger.
func PartyIDForGSTIN(gstin string) string {
	return sha256Hex("GSTIN:" + strings.ToUpper(strings.TrimSpace(gstin)))
}

// FinancierID is the holder ID of a financier organisation.
func FinancierID(msp string) string { return "FIN:" + msp }

func getCaller(ctx contractapi.TransactionContextInterface) (Caller, error) {
	ci := ctx.GetClientIdentity()
	msp, err := ci.GetMSPID()
	if err != nil {
		return Caller{}, fmt.Errorf("read MSP ID: %w", err)
	}
	role, ok, err := ci.GetAttributeValue(attrRole)
	if err != nil || !ok {
		return Caller{}, fmt.Errorf("identity has no %s attribute", attrRole)
	}
	c := Caller{MSP: msp, Role: Role(role)}
	switch c.Role {
	case RoleSupplier, RoleBuyer:
		gstin, ok, err := ci.GetAttributeValue(attrGSTIN)
		if err != nil || !ok || gstin == "" {
			return Caller{}, fmt.Errorf("%s identity has no %s attribute", role, attrGSTIN)
		}
		c.PartyID = PartyIDForGSTIN(gstin)
	case RoleFinancier:
		c.PartyID = FinancierID(msp)
	case RoleRiskOracle, RoleOperator:
		c.PartyID = msp
	default:
		return Caller{}, fmt.Errorf("unknown role %q", role)
	}
	return c, nil
}

func requireRole(ctx contractapi.TransactionContextInterface, roles ...Role) (Caller, error) {
	c, err := getCaller(ctx)
	if err != nil {
		return c, err
	}
	for _, r := range roles {
		if c.Role == r {
			return c, checkPrivilegedMSP(ctx, c)
		}
	}
	return c, fmt.Errorf("role %s may not call this function (allowed: %v)", c.Role, roles)
}

// checkPrivilegedMSP stops an organisation's own CA from minting operator or
// risk-oracle identities: those roles count only from the MSPs named in InitLedger.
func checkPrivilegedMSP(ctx contractapi.TransactionContextInterface, c Caller) error {
	if c.Role != RoleOperator && c.Role != RoleRiskOracle {
		return nil
	}
	cfg, err := getNetworkConfig(ctx)
	if err != nil {
		return err
	}
	if c.Role == RoleOperator && c.MSP == cfg.OperatorMSP {
		return nil
	}
	if c.Role == RoleRiskOracle {
		for _, m := range cfg.RiskOracleMSPs {
			if c.MSP == m {
				return nil
			}
		}
	}
	return fmt.Errorf("%s is not an authorised %s organisation", c.MSP, c.Role)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
