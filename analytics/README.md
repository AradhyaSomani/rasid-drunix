# RASID analytics

Keeps a SQL copy of ledger state for lender analytics: exposure by buyer,
maturity ladder, buyer concentration and MSMED 45-day breaches. The schema runs
on YugabyteDB (YSQL) or PostgreSQL; the dev default is SQLite, so it needs only
Python 3.10+.

```bash
cd analytics
python3 indexer.py --gateway http://localhost:8080 --db rasid.db \
  --identity '{"msp":"CitiMSP","role":"financier"}'          # --identity only for devnet
python3 indexer.py --db rasid.db --report exposure_by_buyer --holder FIN:CitiMSP
python3 indexer.py --dsn postgresql://yugabyte@localhost:5433/yugabyte --gateway ...   # needs psycopg
```

It backfills every receivable, then follows the event stream and re-reads each
receivable it hears about. Reports live in `queries.sql`.

Tested on SQLite (unit tests, plus a live run against the devnet demo). Not yet
run against YugabyteDB.
