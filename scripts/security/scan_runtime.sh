#!/usr/bin/env bash
# Preserve raw findings; only freshly proven absent Go packages may be cleared.
set -euo pipefail
service=${1:?runtime service}
image=${2:?local-registry image}
output=${3:?fresh evidence directory}
mkdir -p "$output"
for variable in ${!TRIVY_@}; do unset "$variable"; done
python3 scripts/check_secret_scanner.py "$output/trivy-secret.yaml"
common=(--config '' --ignorefile '' --ignore-policy ''
  --secret-config "$output/trivy-secret.yaml" --scanners vuln,secret
  --pkg-types os,library --detection-priority comprehensive
  --severity UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL --ignore-unfixed=false
  --list-all-pkgs --format json --image-src docker --no-progress --timeout 15m)
# Exit zero applies only to findings collection. Scanner errors still fail here;
# all findings are assessed by the mandatory final strict inventory gate below.
trivy image "${common[@]}" --exit-code 0 --output "$output/raw.json" "$image" 2> "$output/raw.stderr" || { cat "$output/raw.stderr" >&2; exit 1; }
cat "$output/raw.stderr" >&2
python3 scripts/security/check_scanner_errors.py "$output/raw.stderr"
scripts/security/go_package_proof.sh "$image" "$output/raw.json" "$output/proof"
vex=()
if python3 - "$output/proof/bound.vex.json" <<'PY'
import json, sys
sys.exit(0 if json.load(open(sys.argv[1]))['statements'] else 1)
PY
then
  vex=(--vex "$output/proof/bound.vex.json")
fi
trivy image "${common[@]}" "${vex[@]}" --exit-code 0 --output "$output/final.json" "$image" 2> "$output/final.stderr" || { cat "$output/final.stderr" >&2; exit 1; }
cat "$output/final.stderr" >&2
python3 scripts/security/check_scanner_errors.py "$output/final.stderr"
python3 scripts/security/go_package_proof.py compare "$output/raw.json" "$output/final.json" "$output/proof/bound.vex.json"
python3 scripts/check_scan_inventory.py "$output/final.json" "$service"
