'use strict';

// Registers IRP-signed e-invoices at a fixed rate: the month-end spike workload.
// Each transaction signs a fresh invoice with the mock IRP key, so every
// registration is unique and passes the chaincode's signature check.

const crypto = require('crypto');
const fs = require('fs');
const path = require('path');
const { WorkloadModuleBase } = require('@hyperledger/caliper-core');

const b64url = (buf) => Buffer.from(buf).toString('base64url');

class RegisterInvoiceWorkload extends WorkloadModuleBase {
    async initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext) {
        await super.initializeWorkloadModule(workerIndex, totalWorkers, roundIndex, roundArguments, sutAdapter, sutContext);
        // Relative key paths are taken from the benchmark workspace (this folder's parent).
        this.key = fs.readFileSync(path.resolve(__dirname, '..', roundArguments.irpPrivateKey), 'utf8');
        this.seller = roundArguments.sellerGstin;
        this.buyer = roundArguments.buyerGstin;
        this.identity = roundArguments.supplierIdentity;
        this.mspId = roundArguments.supplierMspId;
        this.counter = 0;
        const today = new Date();
        this.docDt = `${String(today.getUTCDate()).padStart(2, '0')}/${String(today.getUTCMonth() + 1).padStart(2, '0')}/${today.getUTCFullYear()}`;
        this.due = new Date(Date.now() + 60 * 864e5).toISOString().slice(0, 10);
        this.run = crypto.randomBytes(3).toString('hex');
    }

    signedQR() {
        this.counter++;
        const docNo = `BENCH/${this.run}/${this.workerIndex}/${this.counter}`;
        const data = JSON.stringify({
            SellerGstin: this.seller, BuyerGstin: this.buyer, DocNo: docNo, DocTyp: 'INV', DocDt: this.docDt,
            TotInvVal: 100000 + (this.counter % 900000), ItemCnt: 1, MainHsnCode: '8481',
            Irn: crypto.createHash('sha256').update(docNo).digest('hex'),
        });
        const signing = `${b64url(JSON.stringify({ alg: 'RS256', kid: 'irp-2026', typ: 'JWT' }))}.${b64url(JSON.stringify({ data, iss: 'NIC' }))}`;
        const sig = crypto.sign('sha256', Buffer.from(signing), this.key);
        return `${signing}.${b64url(sig)}`;
    }

    async submitTransaction() {
        await this.sutAdapter.sendRequests({
            contractId: 'rasid',
            contractFunction: 'InvoiceRegistry:RegisterEInvoice',
            contractArguments: [this.signedQR(), this.due],
            invokerIdentity: this.identity,
            invokerMspId: this.mspId,
            readOnly: false,
        });
    }
}

module.exports.createWorkloadModule = () => new RegisterInvoiceWorkload();
