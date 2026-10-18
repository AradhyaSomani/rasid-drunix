'use strict';

// Read-only duplicate lookups, the check every lender runs before financing
// an invoice anywhere: CheckDuplicate(sellerGstin, docNo, invoiceDate).

const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

class CheckDuplicateWorkload extends WorkloadModuleBase {
    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        this.args = roundArguments;
        this.counter = 0;
    }

    async submitTransaction() {
        this.counter++;
        await this.sutAdapter.sendRequests({
            contractId: 'rasid',
            contractFunction: 'InvoiceRegistry:CheckDuplicate',
            contractArguments: [this.args.sellerGstin, `LOOKUP/${this.workerIndex}/${this.counter}`, new Date().toISOString().slice(0, 10)],
            invokerIdentity: this.args.financierIdentity,
            invokerMspId: this.args.financierMspId,
            readOnly: true,
        });
    }
}

module.exports.createWorkloadModule = () => new CheckDuplicateWorkload();
