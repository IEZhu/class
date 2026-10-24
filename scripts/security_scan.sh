#!/usr/bin/env bash
set -euo pipefail

target=${1:?source, web or backend required}
evidence=${2:?evidence directory required}
mkdir -p "$evidence"
# Never let a successful process which wrote nothing reuse an earlier report.
rm -f -- "$evidence/report.json"
args=(--config '' --ignore-policy '' --scanners vuln --severity HIGH,CRITICAL
      --ignore-unfixed --no-progress --skip-version-check --list-all-pkgs
      --format json --output /evidence/report.json --exit-code 1)
ignore=''
if [[ -f .trivyignore.yaml ]]; then ignore=/src/.trivyignore.yaml; fi
args+=(--ignorefile "$ignore")
mounts=(-v "$PWD:/src:ro" -v "$evidence:/evidence")
case "$target" in
  source)
    python3 scripts/check_security_scan.py source-inventory . "$evidence/expected.json"
    scan=(fs --include-dev-deps)
    artifact=/src
    ;;
  web|backend)
    python3 scripts/check_security_scan.py image-inventory "$target" security-scan:ci "$evidence"
    mounts+=(-v /var/run/docker.sock:/var/run/docker.sock:ro)
    scan=(image --image-src docker)
    artifact=security-scan:ci
    ;;
  *) echo "Unknown scan target: $target" >&2; exit 1 ;;
esac
# A scanner error or finding is always fatal, even if it wrote a partial report.
if ! docker run --rm "${mounts[@]}" "$TRIVY_IMAGE" "${scan[@]}" "${args[@]}" "$artifact" 2> "$evidence/scanner.stderr"; then
  cat "$evidence/scanner.stderr" >&2
  exit 1
fi
cat "$evidence/scanner.stderr" >&2
python3 scripts/check_security_scan.py check "$evidence/report.json" "$evidence/expected.json" "$evidence/scanner.stderr"
