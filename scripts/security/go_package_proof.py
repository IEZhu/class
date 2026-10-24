#!/usr/bin/env python3
"""Bind fresh package-absence proofs to one exact image and all its Go binaries.

This accepts only vulnerable_code_not_present at package scan level, never a
present-but-unreachable vulnerable function. It does not maintain a CVE list.
"""

from __future__ import annotations

import copy
import hashlib
import json
import re
import sys
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from urllib.parse import quote, unquote

TOOL = "https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck"
GO_VERSION = re.compile(r"v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?")
FIRST_PARTY_MODULE = "github.com/IEZhu/class/backend"


def first_party_root(package: dict) -> bool:
    return (
        package.get("Name") == FIRST_PARTY_MODULE
        and package.get("Version", "") in ("", "(devel)")
        and package.get("Relationship") == "root"
        and package.get("AnalyzedBy") == "gobinary"
        and bool(package.get("DependsOn"))
        and canonical_purl(package.get("Identifier", {}).get("PURL", ""))
        == "pkg:golang/" + FIRST_PARTY_MODULE.lower()
    )


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def key(target: str) -> str:
    if target.startswith("/") or ".." in PurePosixPath(target).parts or "\n" in target:
        raise ValueError("unsafe Go binary target")
    return hashlib.sha256(target.encode()).hexdigest()


def go_results(report: dict) -> dict:
    results = {
        r["Target"]: r for r in report.get("Results", []) if r.get("Type") == "gobinary"
    }
    for target, result in results.items():
        key(target)
        if not result.get("Packages"):
            raise ValueError("empty Go binary inventory")
        for package in result["Packages"]:
            if first_party_root(package):
                continue
            if not GO_VERSION.fullmatch(package.get("Version", "")):
                raise ValueError("Go binary has an unversioned module")
            purl = package.get("Identifier", package.get("PkgIdentifier", {})).get(
                "PURL", ""
            )
            # Trivy's Go PURLs normalize module path case, unlike its Name field.
            expected = f"pkg:golang/{package['Name'].lower()}@{package['Version']}"
            if canonical_purl(purl).split("?", 1)[0].split("#", 1)[0] != expected:
                raise ValueError(
                    "Go binary module PURL/version is missing or mismatched"
                )
    return results


def canonical_purl(value: str) -> str:
    # govulncheck encodes the module path's slashes while Trivy does not.
    # Compare their decoded, still-versioned identifiers and emit Trivy's form.
    return unquote(value)


def version(value: str) -> str:
    return "" if value == "(devel)" else value.removeprefix("go").removeprefix("v")


def events(path: Path) -> list[dict]:
    text = path.read_text()
    decoder = json.JSONDecoder()
    items = []
    while text.strip():
        item, end = decoder.raw_decode(text.lstrip())
        items.append(item)
        text = text.lstrip()[end:]
    return items


def validate_binary(result: dict, directory: Path, expected_hash: str) -> list[dict]:
    prefix = directory / key(result["Target"])
    if digest(prefix.with_suffix(".bin")) != expected_hash:
        raise ValueError("binary SHA256 changed after extraction")
    extracted = events(prefix.with_suffix(".extract.json"))
    if (
        len(extracted) != 2
        or extracted[0] != {"name": "govulncheck-extract", "version": "0.1.0"}
        or not extracted[1].get("pkgSymbols")
        or not extracted[1].get("goos")
        or not extracted[1].get("goarch")
    ):
        raise ValueError(
            "binary package symbols/platform unavailable; cannot prove absence"
        )
    messages = events(prefix.with_suffix(".events.json"))
    configs = [m["config"] for m in messages if "config" in m]
    if len(configs) != 1 or any(
        configs[0].get(k) != v
        for k, v in {
            "scanner_name": "govulncheck",
            "scanner_version": "v1.9.0",
            "scan_level": "package",
            "scan_mode": "binary",
            "db": "https://vuln.go.dev",
        }.items()
    ):
        raise ValueError("proof requires pinned package-level binary scan")
    sboms = [m["SBOM"] for m in messages if "SBOM" in m]
    actual = {(p["Name"], version(p.get("Version", ""))) for p in result["Packages"]}
    if len(sboms) != 1:
        raise ValueError("govulncheck binary module inventory missing")
    modules = sboms[0]["modules"]
    if any(
        not GO_VERSION.fullmatch(m.get("version", "").removeprefix("go"))
        and not (
            m.get("path") == FIRST_PARTY_MODULE
            and m.get("version", "") in ("", "(devel)")
            and any(first_party_root(p) for p in result["Packages"])
        )
        for m in modules
    ):
        raise ValueError("govulncheck has an unversioned module")
    observed = {(m["path"], version(m.get("version", ""))) for m in modules}
    if observed != actual:
        raise ValueError(
            f"govulncheck/Trivy module inventory mismatch: {observed ^ actual}"
        )
    for message in messages:
        if "finding" in message:
            trace = message["finding"].get("trace", [])
            if not trace or trace[0].get("package") or trace[0].get("function"):
                raise ValueError("affected package is present; proof cannot clear it")
    document = json.loads(prefix.with_suffix(".vex.json").read_text())
    if document.get("tooling") != TOOL:
        raise ValueError("unexpected proof generator")
    statements = document.get("statements", [])
    for statement in statements:
        if (
            statement.get("status") != "not_affected"
            or statement.get("justification") != "vulnerable_code_not_present"
        ):
            raise ValueError("only package-absence proof is accepted")
    return statements


def image_purl(report: dict, image_id: str, repo_digest: str) -> str:
    metadata = report["Metadata"]
    if (
        not re.fullmatch(r"sha256:[a-f0-9]{64}", image_id)
        or metadata.get("ImageID") != image_id
    ):
        raise ValueError("image ID mismatch")
    if repo_digest not in metadata.get("RepoDigests", []) or not re.fullmatch(
        r".+@sha256:[a-f0-9]{64}", repo_digest
    ):
        raise ValueError("actual OCI repository digest missing or mismatched")
    repository, checksum = repo_digest.rsplit("@", 1)
    architecture = metadata.get("ImageConfig", {}).get("architecture")
    if not architecture:
        raise ValueError("image architecture missing")
    return (
        f"pkg:oci/{repository.rsplit('/', 1)[-1]}@{checksum}"
        f"?arch={quote(architecture, safe='')}&repository_url={quote(repository, safe='/:')}"
    )


def bind(report: dict, manifest: dict, directory: Path) -> dict:
    age = datetime.now(timezone.utc) - datetime.fromisoformat(manifest["created_at"])
    if not 0 <= age.total_seconds() <= 3600:
        raise ValueError("stale proof run")
    product = image_purl(report, manifest["image_id"], manifest["repo_digest"])
    if (
        hashlib.sha256(json.dumps(report, sort_keys=True).encode()).hexdigest()
        != manifest["report_sha256"]
    ):
        raise ValueError("proof belongs to another report")
    results = go_results(report)
    if set(results) != set(manifest["binaries"]):
        raise ValueError("not every Go binary has proof")
    inventories = {}
    proofs = {}
    templates = {}
    for target, result in results.items():
        inventories[target] = {
            canonical_purl(
                p["Identifier"]["PURL"]
                if "Identifier" in p
                else p["PkgIdentifier"]["PURL"]
            )
            for p in result["Packages"]
            if p.get("Identifier", p.get("PkgIdentifier", {})).get("PURL")
        }
        proofs[target] = set()
        for statement in validate_binary(
            result, directory, manifest["binaries"][target]
        ):
            vuln = statement["vulnerability"]
            ids = {vuln["name"], *vuln.get("aliases", [])}
            for component in statement["products"]:
                for subcomponent in component.get("subcomponents", []):
                    purl = canonical_purl(subcomponent["@id"])
                    if purl not in inventories[target] or "@" not in purl:
                        raise ValueError(
                            "proof dependency PURL/version missing from binary"
                        )
                    for identifier in ids:
                        proofs[target].add((identifier, purl))
                        templates[identifier, purl] = statement
    allowed = set.union(set(), *proofs.values())
    allowed = {
        item
        for item in allowed
        if all(
            item[1] not in inventories[target] or item in proofs[target]
            for target in results
        )
    }
    statements = []
    for identifier, purl in sorted(allowed):
        statement = copy.deepcopy(templates[identifier, purl])
        statement["vulnerability"]["@id"] = identifier
        statement["products"] = [{"@id": product, "subcomponents": [{"@id": purl}]}]
        statement["impact_statement"] = (
            "Pinned govulncheck package scans prove the affected package is absent "
            "from every inventoried Go binary containing this dependency in this exact image. "
            f"ImageID={manifest['image_id']}; repositoryDigest={manifest['repo_digest']}."
        )
        statements.append(statement)
    return {
        "@context": "https://openvex.dev/ns/v0.2.0",
        "@id": f"urn:sha256:{manifest['report_sha256']}",
        "author": "BaRenta CI exact-artifact package analysis",
        "timestamp": manifest["created_at"],
        "version": 1,
        "tooling": TOOL,
        "statements": statements,
    }


def compare(raw: dict, final: dict, vex: dict) -> None:
    if raw["Metadata"]["ImageID"] != final["Metadata"]["ImageID"]:
        raise ValueError("final scan image changed")

    def inventory(report):
        return {
            (r["Target"], r.get("Type"), p["Name"], p.get("Version", ""))
            for r in report["Results"]
            for p in r.get("Packages", [])
        }

    if inventory(raw) != inventory(final):
        raise ValueError("final scan removed or changed package inventory")
    go_results(raw)
    go_results(final)
    allowed = {
        (s["vulnerability"]["@id"], c["@id"])
        for s in vex["statements"]
        for p in s["products"]
        for c in p["subcomponents"]
    }

    def findings(report):
        return {
            (
                r["Target"],
                v["VulnerabilityID"],
                canonical_purl(v.get("PkgIdentifier", {}).get("PURL", "")),
            )
            for r in report["Results"]
            for v in r.get("Vulnerabilities", [])
        }

    for target, identifier, purl in findings(raw) - findings(final):
        if target not in go_results(raw) or (identifier, purl) not in allowed:
            raise ValueError(
                "final scan cleared a finding without exact package-absence proof"
            )


def main() -> None:
    command, *args = sys.argv[1:]
    if command == "prepare":
        report_path, image_id, repo_digest, output = args
        report = json.loads(Path(report_path).read_text())
        image_purl(report, image_id, repo_digest)
        directory = Path(output)
        directory.mkdir(parents=True, exist_ok=False)
        manifest = {
            "created_at": datetime.now(timezone.utc).isoformat(),
            "image_id": image_id,
            "repo_digest": repo_digest,
            "binaries": {},
            "report_sha256": hashlib.sha256(
                json.dumps(report, sort_keys=True).encode()
            ).hexdigest(),
        }
        (directory / "manifest.json").write_text(json.dumps(manifest, indent=2))
        for target in go_results(report):
            print(target)
    elif command == "key":
        print(key(args[0]))
    elif command == "record":
        directory, target = Path(args[0]), args[1]
        manifest = json.loads((directory / "manifest.json").read_text())
        manifest["binaries"][target] = digest(directory / (key(target) + ".bin"))
        (directory / "manifest.json").write_text(json.dumps(manifest, indent=2))
    elif command == "bind":
        report_path, output = args
        directory = Path(output)
        report = json.loads(Path(report_path).read_text())
        manifest = json.loads((directory / "manifest.json").read_text())
        (directory / "bound.vex.json").write_text(
            json.dumps(bind(report, manifest, directory), indent=2)
        )
    elif command == "compare":
        compare(*(json.loads(Path(p).read_text()) for p in args))
    else:
        raise ValueError("unknown command")


if __name__ == "__main__":
    main()
