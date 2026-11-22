#!/usr/bin/env bash
# Couverture par package à partir de profils `go test -coverpkg=... -coverprofile=...`.
# Un bloc est couvert si un test, de n'importe quel package, l'exécute : les tests
# d'intégration de internal/api comptent donc pour internal/auth, etc.
# Usage : .github/coverage.sh cover.out [autres profils...]
# Écrit un tableau Markdown sur stdout ; code 1 si un package cible est sous le seuil.
set -euo pipefail

MIN=${COVERAGE_MIN:-80}
# Packages visés par PLAN.md §9.
TARGETS=${COVERAGE_TARGETS:-"internal/engine/rsync internal/runner internal/paths internal/auth internal/authz"}

rows=$(awk '
  /^mode:/ { next }
  {
    key = $1; stmts[key] = $2
    if ($3 > 0) hit[key] = 1
  }
  END {
    for (k in stmts) {
      pkg = k; sub(/:.*/, "", pkg); sub(/\/[^\/]+$/, "", pkg); sub(/^rcsui\//, "", pkg)
      tot[pkg] += stmts[k]
      if (k in hit) cov[pkg] += stmts[k]
    }
    for (p in tot) if (tot[p] > 0) printf "%s %.1f\n", p, 100 * cov[p] / tot[p]
  }
' "$@" | sort)

fail=0
echo "| Package | Couverture | Cible ≥ ${MIN} % |"
echo "|---|---|---|"
while read -r pkg pct; do
  mark=""
  for t in $TARGETS; do
    if [ "$pkg" = "$t" ]; then
      if awk -v p="$pct" -v m="$MIN" 'BEGIN { exit !(p < m) }'; then mark="❌"; fail=1; else mark="✅"; fi
    fi
  done
  echo "| \`$pkg\` | $pct % | $mark |"
done <<< "$rows"

for t in $TARGETS; do
  if ! grep -q "^$t " <<< "$rows"; then
    echo "| \`$t\` | absent | ❌ |"
    fail=1
  fi
done
exit $fail
