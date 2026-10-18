package contracts

import (
	"fmt"
	"strconv"

	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

// ReceivableToken tracks fractional ownership. Every receivable has
// TotalUnits units; the supplier holds them all at registration and they move
// to financiers when funding is confirmed.
type ReceivableToken struct {
	contractapi.Contract
}

func holdingKey(ctx contractapi.TransactionContextInterface, id, holder string) (string, error) {
	return compositeKey(ctx, keyHolding, id, holder)
}

func getHolding(ctx contractapi.TransactionContextInterface, id, holder string) (int64, error) {
	key, err := holdingKey(ctx, id, holder)
	if err != nil {
		return 0, err
	}
	b, err := ctx.GetStub().GetState(key)
	if err != nil || b == nil {
		return 0, err
	}
	return strconv.ParseInt(string(b), 10, 64)
}

func setHolding(ctx contractapi.TransactionContextInterface, id, holder string, units int64) error {
	key, err := holdingKey(ctx, id, holder)
	if err != nil {
		return err
	}
	if units == 0 {
		return ctx.GetStub().DelState(key)
	}
	return ctx.GetStub().PutState(key, []byte(strconv.FormatInt(units, 10)))
}

func moveUnits(ctx contractapi.TransactionContextInterface, id, from, to string, units int64) error {
	if units <= 0 {
		return fmt.Errorf("units must be positive")
	}
	if from == to {
		return fmt.Errorf("cannot transfer to the same holder")
	}
	have, err := getHolding(ctx, id, from)
	if err != nil {
		return err
	}
	if have < units {
		return fmt.Errorf("%s holds %d units of %s, cannot move %d", from, have, id, units)
	}
	toHave, err := getHolding(ctx, id, to)
	if err != nil {
		return err
	}
	if err := setHolding(ctx, id, from, have-units); err != nil {
		return err
	}
	return setHolding(ctx, id, to, toHave+units)
}

func listHoldings(ctx contractapi.TransactionContextInterface, id string) ([]Holding, error) {
	it, err := ctx.GetStub().GetStateByPartialCompositeKey(keyHolding, []string{id})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	out := []Holding{}
	for it.HasNext() {
		kv, err := it.Next()
		if err != nil {
			return nil, err
		}
		_, attrs, err := ctx.GetStub().SplitCompositeKey(kv.Key)
		if err != nil {
			return nil, err
		}
		units, err := strconv.ParseInt(string(kv.Value), 10, 64)
		if err != nil {
			return nil, err
		}
		out = append(out, Holding{ReceivableID: id, HolderID: attrs[1], Units: units})
	}
	return out, nil
}

// GetHoldings lists who owns how many units of a receivable.
func (c *ReceivableToken) GetHoldings(ctx contractapi.TransactionContextInterface, id string) ([]Holding, error) {
	return listHoldings(ctx, id)
}

// TransferUnits sells units to another financier organisation, for example an
// NBFC selling part of its book to a bank. Payment for the sale is settled
// off-ledger between the two institutions.
func (c *ReceivableToken) TransferUnits(ctx contractapi.TransactionContextInterface, id, toMSP string, units int64) error {
	caller, err := requireRole(ctx, RoleFinancier)
	if err != nil {
		return err
	}
	r, err := getReceivable(ctx, id)
	if err != nil {
		return err
	}
	if err := r.requireStatus(StatusFunded, StatusOverdue); err != nil {
		return err
	}
	if toMSP == "" {
		return fmt.Errorf("recipient MSP is required")
	}
	to := FinancierID(toMSP)
	if err := moveUnits(ctx, id, caller.PartyID, to, units); err != nil {
		return err
	}
	return emit(ctx, "UnitsTransferred", map[string]any{"id": id, "from": caller.PartyID, "to": to, "units": units})
}
