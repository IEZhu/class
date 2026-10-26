#!/usr/bin/env bash
# An ephemeral local registry gives locally built images real OCI digest/PURLs.
# Analyze every Go binary, not just the service entrypoint.
set -euo pipefail
image=${1:?digest-pinned local-registry image}
report=${2:?raw Trivy JSON}
output=${3:?fresh proof directory}
image_id=$(docker image inspect --format '{{.Id}}' "$image")
repo_digest=$(docker image inspect --format '{{index .RepoDigests 0}}' "$image")
python3 scripts/security/go_package_proof.py prepare "$report" "$image_id" "$repo_digest" "$output" > "$output.targets"
container=$(docker create --network none "$image_id")
trap 'docker rm -v "$container" >/dev/null' EXIT
while IFS= read -r target; do
  prefix="$output/$(python3 scripts/security/go_package_proof.py key "$target")"
  docker cp "$container:/$target" "$prefix.bin"
  python3 scripts/security/go_package_proof.py record "$output" "$target"
  govulncheck -mode=extract "$prefix.bin" > "$prefix.extract.json"
  # Text mode is a required gate: JSON/OpenVEX alone return zero for real vulnerabilities.
  govulncheck -mode=binary -scan=package "$prefix.bin" > "$prefix.gate.txt"
  govulncheck -mode=binary -scan=package -format=json "$prefix.bin" > "$prefix.events.json"
  govulncheck -mode=binary -scan=package -format=openvex "$prefix.bin" > "$prefix.vex.json"
done < "$output.targets"
python3 scripts/security/go_package_proof.py bind "$report" "$output"
