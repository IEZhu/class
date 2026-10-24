#!/usr/bin/env bash
# Prove the package gate rejects OpenPGP when its code really is in the binary.
set -euo pipefail
work=${1:?fresh control directory}
mkdir -p "$work"
cat > "$work/go.mod" <<'EOF'
module barenta-security-positive-control

go 1.27.0

require golang.org/x/crypto v0.57.0
EOF
cat > "$work/main.go" <<'EOF'
package main
import (
  "os"
  "golang.org/x/crypto/openpgp"
)
func main() { _, _ = openpgp.ReadKeyRing(os.Stdin) }
EOF
(cd "$work" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=mod -o control .)
# A network/tool failure cannot satisfy the control: OpenVEX must also explicitly
# report the real OpenPGP advisory as affected.
if govulncheck -mode=binary -scan=package "$work/control" > "$work/text.txt"; then
  echo 'Positive control unexpectedly passed' >&2
  exit 1
fi
govulncheck -mode=binary -scan=package -format=openvex "$work/control" > "$work/vex.json"
if (cd "$work" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 govulncheck -mode=source -scan=package -test ./...) > "$work/source-text.txt"; then
  echo 'Source positive control unexpectedly passed' >&2
  exit 1
fi
(cd "$work" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 govulncheck -mode=source -scan=package -test -format=openvex ./...) > "$work/source-vex.json"
python3 - "$work/vex.json" "$work/source-vex.json" <<'PY'
import json, sys
for path in sys.argv[1:]:
    statements = json.load(open(path)).get('statements', [])
    assert any(s['vulnerability']['name'] == 'GO-2026-5932' and s['status'] == 'affected' for s in statements), 'OpenPGP affected control missing'
PY
