"""Reject successful-looking scans which missed an application's inventory."""

import json
import sys


def check(report, target, package_absence=()):
    results = report.get("Results")
    if not isinstance(results, list) or not results:
        raise ValueError("missing scan results")
    packages = [p for r in results for p in r.get("Packages", [])]
    def own_source_root(package):
        name = package.get("Name")
        return (
            name in ("github.com/IEZhu/class/backend", "github.com/IEZhu/class/build/gosu")
            and package.get("Version", "") in ("", "(devel)")
            and package.get("Relationship") == "root"
            and package.get("AnalyzedBy") in ("gomod", "gobinary")
            and (name == "github.com/IEZhu/class/backend" or package.get("AnalyzedBy") == "gomod")
            and package.get("DependsOn")
            and package.get("Identifier", {}).get("PURL") == "pkg:golang/" + name.lower()
        )

    if not packages or any(
        not p.get("Name") or (
            p.get("Version", "").lower() in ("", "(devel)", "devel", "unknown")
            and not own_source_root(p)
        )
        for p in packages
    ):
        raise ValueError("missing or unversioned package inventory")
    if target == "source":
        for manifest in ("backend/go.mod", "web/package-lock.json"):
            if not any(r.get("Target", "").endswith(manifest) and r.get("Packages") for r in results):
                raise ValueError(f"manifest was not scanned: {manifest}")
    elif target in ("api", "caddy", "postgres"):
        if not any(r.get("Class") == "os-pkgs" and r.get("Packages") for r in results):
            raise ValueError("runtime OS inventory missing")
        if target in ("api", "caddy") and not any(
            r.get("Type") == "gobinary" and any(
                p["Name"] == "stdlib" for p in r.get("Packages", [])
            )
            for r in results
        ):
            raise ValueError("compiled Go runtime inventory missing")
    elif target == "web":
        if not any(r.get("Class") == "os-pkgs" and r.get("Packages") for r in results):
            raise ValueError("runtime OS inventory missing")
        for package in ("nodejs", "next", "react"):
            if not any(p["Name"] == package for p in packages):
                raise ValueError(f"runtime package missing: {package}")
    else:
        raise ValueError(f"unknown scan target: {target}")
    from urllib.parse import unquote
    if any(
        r.get("Secrets") or any(
            (r.get("Target"), v.get("VulnerabilityID"), unquote(v.get("PkgIdentifier", {}).get("PURL", "")))
            not in package_absence
            for v in r.get("Vulnerabilities", [])
        )
        for r in results
    ):
        raise ValueError("scan findings must block the pipeline")
    print(f"Verified {target}: {len(packages)} packages, zero applicable findings")


if __name__ == "__main__":
    with open(sys.argv[1], encoding="utf-8") as stream:
        check(json.load(stream), sys.argv[2])
