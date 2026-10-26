#!/usr/bin/env python3
"""Gate complete source findings with a fresh, content-bound Linux Go graph.

The original Trivy report remains intact. Only module advisories for which the
official package scan explicitly proves absent code can pass the final gate.
"""

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
from datetime import datetime, timezone

from go_package_proof import TOOL, canonical_purl, events, version

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from check_scan_inventory import check

MODULE = "github.com/IEZhu/class/backend"
GO_VERSION = "go1.26.10"
ENVIRONMENT = {
    "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0",
    "GOTOOLCHAIN": GO_VERSION, "GOFLAGS": "-mod=readonly", "GOWORK": "off",
    "GOAMD64": "v1", "GOEXPERIMENT": "",
}
OUTPUTS = {"modules.txt", "packages.json", "gate.txt", "events.json", "vex.json"}


def sha(data):
    return hashlib.sha256(data).hexdigest()


def snapshot(root):
    paths = sorted(root.rglob("*"))
    if any(p.is_symlink() for p in paths):
        raise ValueError("source proof does not accept symlinked inputs")
    files = {str(p.relative_to(root)): sha(p.read_bytes()) for p in paths if p.is_file()}
    if not {"go.mod", "go.sum"} <= files.keys() or not any(p.endswith(".go") for p in files):
        raise ValueError("source snapshot is incomplete")
    return files


def validate(report, root, output):
    manifest = json.loads((output / "manifest.json").read_text())
    age = datetime.now(timezone.utc) - datetime.fromisoformat(manifest["created_at"])
    if not 0 <= age.total_seconds() <= 3600:
        raise ValueError("stale source proof")
    files = snapshot(root)
    if files != manifest["source_files"] or manifest["environment"] != ENVIRONMENT:
        raise ValueError("source inputs or target platform changed")
    if manifest["report_sha256"] != sha(json.dumps(report, sort_keys=True).encode()):
        raise ValueError("source proof belongs to another report")
    if manifest["outputs"].keys() != OUTPUTS:
        raise ValueError("source proof output coverage is incomplete")
    for name, checksum in manifest["outputs"].items():
        if sha((output / name).read_bytes()) != checksum:
            raise ValueError("source analyzer output changed")
    if (output / "modules.txt").read_text().strip() != "all modules verified":
        raise ValueError("source module checksum verification missing")
    if (output / "gate.txt").read_text().strip() == "":
        raise ValueError("mandatory text gate output missing")
    graph = events(output / "packages.json")
    if not graph or any(p.get("Error") or p.get("DepsErrors") or p.get("Incomplete") for p in graph):
        raise ValueError("source package graph is incomplete")
    if any(p.get("Module", {}).get("Replace") for p in graph):
        raise ValueError("replaced dependencies cannot establish package absence")
    if not any(p.get("ImportPath") == MODULE + "/internal/httpapi.test" for p in graph):
        raise ValueError("test package graph missing")
    messages = events(output / "events.json")
    if any("error" in message for message in messages):
        raise ValueError("source analyzer reported an error")
    configs = [m["config"] for m in messages if "config" in m]
    expected = {
        "scanner_name": "govulncheck", "scanner_version": "v1.9.0",
        "scan_level": "package", "scan_mode": "source",
        "db": "https://vuln.go.dev", "go_version": GO_VERSION,
    }
    if len(configs) != 1 or any(configs[0].get(k) != v for k, v in expected.items()):
        raise ValueError("requires pinned package-level source analysis")
    source_results = [r for r in report["Results"] if r.get("Target", "").endswith("backend/go.mod")]
    if len(source_results) != 1 or source_results[0].get("Type") != "gomod":
        raise ValueError("one complete Go manifest inventory is required")
    result = source_results[0]
    inventory = {(p["Name"], version(p.get("Version", ""))) for p in result["Packages"]}
    if any(not v and n != MODULE for n, v in inventory):
        raise ValueError("unknown third-party source dependency version")
    sboms = [m["SBOM"] for m in messages if "SBOM" in m]
    if len(sboms) != 1 or not sboms[0].get("roots") or not sboms[0].get("modules"):
        raise ValueError("source analyzer inventory missing")
    observed = {(p["path"], version(p.get("version", ""))) for p in sboms[0]["modules"]}
    if not observed <= inventory or (MODULE, "") not in observed:
        raise ValueError("source analyzer inventory/version differs from locked manifest")
    modules = {p.get("Module", {}).get("Path") for p in graph if p.get("Module")}
    if not modules <= {n for n, _ in observed}:
        raise ValueError("source graph modules missing from analyzer inventory")
    for message in messages:
        if "finding" in message:
            trace = message["finding"].get("trace", [])
            if not trace or trace[0].get("package") or trace[0].get("function"):
                raise ValueError("affected package is present in source/test graph")
    document = json.loads((output / "vex.json").read_text())
    if document.get("tooling") != TOOL:
        raise ValueError("unexpected source proof generator")
    purls = {
        canonical_purl(p.get("Identifier", p.get("PkgIdentifier", {})).get("PURL", ""))
        for p in result["Packages"]
    }
    allowed = set()
    for statement in document.get("statements", []):
        if statement.get("status") != "not_affected" or statement.get("justification") != "vulnerable_code_not_present":
            raise ValueError("only absent affected packages can pass")
        vulnerability = statement["vulnerability"]
        ids = {vulnerability["name"], *vulnerability.get("aliases", [])}
        for product in statement["products"]:
            for component in product.get("subcomponents", []):
                purl = canonical_purl(component["@id"])
                if purl not in purls or "@" not in purl:
                    raise ValueError("source proof dependency/version missing from inventory")
                allowed.update((result["Target"], identifier, purl) for identifier in ids)
    return allowed


def run(root, report_path, output):
    root = root.resolve()
    report = json.loads(report_path.read_text())
    before = snapshot(root)
    output.mkdir(parents=True, exist_ok=False)
    environment = dict(os.environ, **ENVIRONMENT)
    commands = {
        "modules.txt": ["go", "mod", "verify"],
        "packages.json": ["go", "list", "-deps", "-test", "-json", "./..."],
        "gate.txt": ["govulncheck", "-mode=source", "-scan=package", "-test", "./..."],
        "events.json": ["govulncheck", "-mode=source", "-scan=package", "-test", "-format=json", "./..."],
        "vex.json": ["govulncheck", "-mode=source", "-scan=package", "-test", "-format=openvex", "./..."],
    }
    for name, command in commands.items():
        with (output / name).open("wb") as stream, (output / (name + ".stderr")).open("wb") as errors:
            subprocess.run(command, cwd=root, env=environment, stdout=stream, stderr=errors, check=True, timeout=600)
    if snapshot(root) != before:
        raise ValueError("source changed during analysis")
    manifest = {
        "created_at": datetime.now(timezone.utc).isoformat(), "source_files": before,
        "environment": ENVIRONMENT, "report_sha256": sha(json.dumps(report, sort_keys=True).encode()),
        "outputs": {name: sha((output / name).read_bytes()) for name in commands},
    }
    manifest["product"] = "pkg:generic/class-backend-source@" + sha(json.dumps(before, sort_keys=True).encode())
    (output / "manifest.json").write_text(json.dumps(manifest, indent=2))
    allowed = validate(report, root, output)
    # No vulnerabilities are removed from the original scan artifact.
    check(report, "source", allowed)
    (output / "applicability.json").write_text(json.dumps({
        "product": manifest["product"], "report_sha256": manifest["report_sha256"],
        "not_affected": sorted(allowed), "reason": "vulnerable_code_not_present",
    }, indent=2))


if __name__ == "__main__":
    run(*(Path(p) for p in sys.argv[1:]))
