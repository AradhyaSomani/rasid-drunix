#!/usr/bin/env bash
# Runs every RASID check and prints a pass/fail table. Run it after every
# change; it must end with "All checks passed" before anything is pushed.
#
#   bash scripts/check.sh            everything
#   bash scripts/check.sh --quick    static checks and unit tests only (about a minute)
#
# Needs Go 1.24+, Python 3.9+, curl and jq. The browser test also needs Node
# and Playwright (cd tests/ui && npm install && npx playwright install chromium);
# without them it is skipped, except on CI, where a skip counts as a failure.
set -uo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
QUICK=0
[ "${1:-}" = "--quick" ] && QUICK=1
LOGS=$(mktemp -d)
RESULTS=()
FAILED=0
GO_MODULES="chaincode/rasid app/devnet app/gateway tools/mock-irp tools/issue-identity"
PY_SUITES="ai/risk-service adapters/payments analytics"

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
record() {
  RESULTS+=("$(printf '%-6s %-44s %s' "$1" "$2" "$3")")
  if [ "$1" = "FAIL" ]; then FAILED=1; fi
}
run() {
  local name=$1; shift
  local log="$LOGS/$(echo "$name" | tr ' /:' '___').log"
  local start=$SECONDS
  if "$@" >"$log" 2>&1; then
    record PASS "$name" "$((SECONDS - start))s"
  else
    record FAIL "$name" "see $log"
    echo "---- $name failed; last lines:"; tail -20 "$log"; echo "----"
  fi
}

bold "Static checks"
gofmt_all() {
  local bad="" m
  for m in $GO_MODULES; do bad="$bad$(cd "$m" && gofmt -l .)"; done
  if [ -n "$bad" ]; then echo "not gofmt-formatted: $bad"; return 1; fi
}
vet_all() { local m; for m in $GO_MODULES; do (cd "$m" && go vet ./...) || return 1; done; }
bash_syntax() { local f; for f in scripts/*.sh network/*.sh network/local/*.sh network/drunix/*.sh benchmarks/caliper/*.sh benchmarks/caliper/networks/*.sh; do [ -f "$f" ] && { bash -n "$f" || return 1; }; done; return 0; }
shellcheck_all() { shellcheck -S error scripts/*.sh network/*.sh network/local/*.sh network/drunix/*.sh benchmarks/caliper/*.sh benchmarks/caliper/networks/*.sh; }
js_syntax() { node --check app/web/app.js; }
no_secrets() {
  # In a git checkout, look at what git would commit (tracked files and new
  # files that .gitignore doesn't exclude); generated keys such as the
  # benchmark's mock IRP key are ignored on purpose. Elsewhere, every file.
  local files hits
  if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    files=$(git ls-files -co --exclude-standard)
  else
    files=$(find . -name node_modules -prune -o -type f -print | sed 's|^\./||')
  fi
  hits=$(printf '%s\n' "$files" | grep -E '\.(pem|key)$|(^|/)\.env$' || true)
  hits="$hits$(printf '%s\n' "$files" | grep -v '_test\.go$' | tr '\n' '\0' | xargs -0 grep -IlE -- '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----' 2>/dev/null || true)"
  if [ -n "$hits" ]; then echo "private keys or secrets in the tree:"; echo "$hits"; return 1; fi
}
run "gofmt" gofmt_all
run "go vet" vet_all
run "bash syntax" bash_syntax
if command -v shellcheck >/dev/null; then run "shellcheck (errors)" shellcheck_all; else record SKIP "shellcheck (errors)" "shellcheck not installed"; fi
if command -v node >/dev/null; then run "console JavaScript syntax" js_syntax; else record SKIP "console JavaScript syntax" "node not installed"; fi
run "no private keys committed" no_secrets

bold "Unit, security and role tests"
for m in $GO_MODULES; do
  if [ -n "$(find "$m" -name '*_test.go' | head -1)" ]; then
    run "go test -race $m" bash -c "cd '$m' && go test -race -count=1 ./..."
  else
    run "go build $m" bash -c "cd '$m' && go build -o /dev/null ./..."
  fi
done
for p in $PY_SUITES; do
  run "python tests $p" bash -c "cd '$p' && python3 -m unittest discover -s tests -t ."
done

if [ $QUICK = 0 ]; then
  bold "End to end"
  e2e() {
    local out
    out=$(DEVNET_PORT=$((18100 + RANDOM % 500)) RISK_PORT=$((18700 + RANDOM % 500)) bash scripts/demo-devnet.sh 2>&1)
    echo "$out"
    echo "$out" | grep -q "status CLOSED" && [ "$(echo "$out" | grep -c "  paid ")" -ge 2 ]
  }
  run "demo-devnet reaches CLOSED" e2e

  bold "Browser"
  if command -v node >/dev/null && (cd tests/ui && node -e "import('playwright').then(()=>process.exit(0),()=>process.exit(1))") 2>/dev/null; then
    run "console clicked to CLOSED in Chromium" node tests/ui/console.test.mjs
  elif [ -n "${CI:-}" ]; then
    record FAIL "console clicked to CLOSED in Chromium" "Playwright missing on CI"
  else
    record SKIP "console clicked to CLOSED in Chromium" "cd tests/ui && npm install && npx playwright install chromium"
  fi
fi

echo
bold "Results"
for r in "${RESULTS[@]}"; do echo "$r"; done
echo
if [ $FAILED = 0 ]; then
  bold "All checks passed"
  rm -rf "$LOGS"
else
  bold "Some checks FAILED. Logs: $LOGS"
  exit 1
fi
