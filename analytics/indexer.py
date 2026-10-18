"""Indexes RASID ledger state into SQL for lender analytics.

Follows the gateway's event stream; on each event it re-reads the receivable
and its holdings from the chaincode and upserts them. SQLite by default; pass
--dsn to write to YugabyteDB/PostgreSQL (needs psycopg installed).

    python indexer.py --gateway http://localhost:8080 --db rasid.db [--identity '{...}' for devnet]
    python indexer.py --db rasid.db --report exposure_by_buyer --holder FIN:CitiMSP
"""

from __future__ import annotations

import argparse
import json
import re
import sqlite3
import sys
import urllib.parse
import urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent


def load_queries() -> dict[str, str]:
    text = (HERE / "queries.sql").read_text()
    out = {}
    for block in re.split(r"^-- name: ", text, flags=re.M)[1:]:
        name, _, body = block.partition("\n")
        out[name.strip()] = body.strip().rstrip(";")
    return out


class Store:
    def __init__(self, conn) -> None:
        self.conn = conn
        self.pg = not isinstance(conn, sqlite3.Connection)
        cur = conn.cursor()
        schema = "\n".join(l for l in (HERE / "schema.sql").read_text().splitlines() if not l.lstrip().startswith("--"))
        for stmt in schema.split(";"):
            if stmt.strip():
                cur.execute(stmt)
        conn.commit()

    def _sql(self, sql: str) -> str:
        # :name placeholders -> %(name)s for psycopg
        return re.sub(r":(\w+)", r"%(\1)s", sql) if self.pg else sql

    def upsert(self, r: dict, holdings: list[dict]) -> None:
        row = {
            "id": r["id"], "supplier_id": r["supplierId"], "buyer_id": r["buyerId"], "origin_msp": r["originMsp"],
            "e_invoice": bool(r["eInvoice"]), "amount_paise": r["amountPaise"], "funded_paise": r.get("fundedPaise", 0),
            "repaid_paise": r.get("repaidPaise", 0), "invoice_date": r["invoiceDate"], "due_date": r["dueDate"],
            "status": r["status"], "risk_score": (r.get("risk") or {}).get("score"),
            "days_late": r.get("daysLate", 0), "breach_45": bool(r.get("breach45Days")), "updated_at": r["updatedAt"],
        }
        cols = ", ".join(row)
        vals = ", ".join(f":{k}" for k in row)
        updates = ", ".join(f"{k} = excluded.{k}" for k in row if k != "id")
        cur = self.conn.cursor()
        cur.execute(self._sql(f"INSERT INTO receivable ({cols}) VALUES ({vals}) ON CONFLICT (id) DO UPDATE SET {updates}"), row)
        cur.execute(self._sql("DELETE FROM holding WHERE receivable_id = :id"), {"id": r["id"]})
        for h in holdings:
            cur.execute(self._sql("INSERT INTO holding (receivable_id, holder_id, units) VALUES (:r, :h, :u)"),
                        {"r": r["id"], "h": h["holderId"], "u": h["units"]})
        self.conn.commit()

    def report(self, name: str, **params) -> list[tuple]:
        cur = self.conn.cursor()
        cur.execute(self._sql(load_queries()[name]), params)
        return cur.fetchall()


class Gateway:
    def __init__(self, base: str, identity: str | None) -> None:
        self.base = base.rstrip("/")
        self.headers = {"X-Rasid-Identity": identity} if identity else {}

    def query(self, fn: str, *args: str):
        c, f = fn.split(":")
        qs = "&".join("arg=" + urllib.parse.quote(a) for a in args)
        req = urllib.request.Request(f"{self.base}/query/{c}/{f}?{qs}", headers=self.headers)
        with urllib.request.urlopen(req, timeout=30) as r:
            return json.loads(r.read())

    def events(self):
        with urllib.request.urlopen(urllib.request.Request(f"{self.base}/events", headers=self.headers)) as r:
            for raw in r:
                line = raw.decode().strip()
                if line.startswith("data: "):
                    yield json.loads(line[6:])


def sync_one(gw: Gateway, store: Store, rid: str) -> None:
    store.upsert(gw.query("InvoiceRegistry:GetReceivable", rid), gw.query("ReceivableToken:GetHoldings", rid))


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--gateway")
    ap.add_argument("--identity", help="devnet only")
    ap.add_argument("--db", default="rasid.db", help="SQLite file")
    ap.add_argument("--dsn", help="YugabyteDB/PostgreSQL DSN instead of SQLite")
    ap.add_argument("--report", choices=sorted(load_queries()))
    ap.add_argument("--holder", default="")
    a = ap.parse_args()

    if a.dsn:
        import psycopg  # type: ignore

        conn = psycopg.connect(a.dsn)
    else:
        conn = sqlite3.connect(a.db)
    store = Store(conn)

    if a.report:
        for row in store.report(a.report, holder=a.holder):
            print(*row, sep="\t")
        return
    if not a.gateway:
        ap.error("--gateway is required to index")
    gw = Gateway(a.gateway, a.identity)
    bookmark = ""  # backfill page by page, then follow events
    while True:
        page = gw.query("InvoiceRegistry:ListReceivables", "", "200", bookmark)
        for r in page["records"]:
            sync_one(gw, store, r["id"])
        bookmark = page.get("bookmark", "")
        if not bookmark:
            break
    print("backfilled; following events", file=sys.stderr, flush=True)
    for ev in gw.events():
        rid = (ev.get("payload") or {}).get("id")
        if rid:
            sync_one(gw, store, rid)


if __name__ == "__main__":
    main()
