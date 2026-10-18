// Command rasid is the RASID chaincode for DRUNIX. It packages four contracts
// that share one receivable record; call them as "<Contract>:<Function>",
// for example "InvoiceRegistry:RegisterEInvoice".
package main

import (
	"log"
	"os"

	"github.com/AradhyaSomani/rasid-drunix/chaincode/rasid/contracts"
	"github.com/hyperledger/fabric-chaincode-go/v2/shim"
	"github.com/hyperledger/fabric-contract-api-go/v2/contractapi"
)

func main() {
	registry := &contracts.InvoiceRegistry{}
	registry.Name = "InvoiceRegistry"
	market := &contracts.Marketplace{}
	market.Name = "Marketplace"
	token := &contracts.ReceivableToken{}
	token.Name = "ReceivableToken"
	settlement := &contracts.Settlement{}
	settlement.Name = "Settlement"

	cc, err := contractapi.NewChaincode(registry, market, token, settlement)
	if err != nil {
		log.Panicf("create RASID chaincode: %v", err)
	}
	cc.Info.Title = "RASID"
	cc.Info.Version = "0.1.0"
	cc.DefaultContract = registry.GetName()

	// Chaincode-as-a-service when CHAINCODE_SERVER_ADDRESS is set (Falcon/Kubernetes),
	// otherwise the classic peer-launched mode.
	if addr := os.Getenv("CHAINCODE_SERVER_ADDRESS"); addr != "" {
		server := &shim.ChaincodeServer{
			CCID:     os.Getenv("CHAINCODE_ID"),
			Address:  addr,
			CC:       cc,
			TLSProps: shim.TLSProperties{Disabled: true},
		}
		if err := server.Start(); err != nil {
			log.Panicf("start RASID chaincode server: %v", err)
		}
		return
	}
	if err := cc.Start(); err != nil {
		log.Panicf("start RASID chaincode: %v", err)
	}
}
