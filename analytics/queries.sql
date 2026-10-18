-- name: exposure_by_buyer
-- Outstanding financed exposure per buyer, for one financier (holder = 'FIN:<msp>').
SELECT r.buyer_id,
       COUNT(*) AS receivables,
       SUM((r.amount_paise - r.repaid_paise) * h.units / 10000) AS outstanding_paise
FROM receivable r JOIN holding h ON h.receivable_id = r.id
WHERE h.holder_id = :holder AND r.status IN ('FUNDED', 'OVERDUE', 'DEFAULTED')
GROUP BY r.buyer_id
ORDER BY outstanding_paise DESC;

-- name: maturity_ladder
-- Face value falling due per week, across the network.
SELECT due_date, COUNT(*) AS receivables, SUM(amount_paise - repaid_paise) AS due_paise
FROM receivable
WHERE status IN ('FUNDED', 'OVERDUE')
GROUP BY due_date
ORDER BY due_date;

-- name: buyer_concentration
-- Share of all funded receivables owed by each buyer.
SELECT buyer_id,
       SUM(amount_paise - repaid_paise) AS outstanding_paise,
       ROUND(100.0 * SUM(amount_paise - repaid_paise) /
             (SELECT SUM(amount_paise - repaid_paise) FROM receivable WHERE status IN ('FUNDED', 'OVERDUE', 'DEFAULTED')), 2) AS pct
FROM receivable
WHERE status IN ('FUNDED', 'OVERDUE', 'DEFAULTED')
GROUP BY buyer_id
ORDER BY outstanding_paise DESC;

-- name: late_payers
-- Buyers that have breached the MSMED 45-day limit (Section 43B(h) exposure).
SELECT buyer_id, COUNT(*) AS late_invoices, MAX(days_late) AS worst_days_late
FROM receivable
WHERE breach_45
GROUP BY buyer_id
ORDER BY late_invoices DESC;
