"""Reject successful-looking scans which missed an application's inventory."""

import json
import sys


def check(report, target):
    results = report.get("Results")
    if not isinstance(results, list) or not results:
        raise ValueError("missing scan results")
    packages = [p for r in results for p in r.get("Packages", [])]
    def own_source_root(package):
        return (
            package.get("Name") == "github.com/IEZhu/class/backend"
            and package.get("Relationship") == "root"
            and package.get("AnalyzedBy") in ("gomod", "gobinary")
            and package.get("DependsOn")
        )

    if not packages or any(
        not p.get("Name") or (not p.get("Version") and not own_source_root(p))
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
        if target in ("api", "caddy") and not any(p["Name"] == "stdlib" for p in packages):
            raise ValueError("compiled Go runtime inventory missing")
    elif target == "web":
        for package in ("nodejs", "next", "react"):
            if not any(p["Name"] == package for p in packages):
                raise ValueError(f"runtime package missing: {package}")
    else:
        raise ValueError(f"unknown scan target: {target}")
    if any(r.get("Vulnerabilities") or r.get("Secrets") for r in results):
        raise ValueError("scan findings must block the pipeline")
    print(f"Verified {target}: {len(packages)} packages, zero findings")


if __name__ == "__main__":
    with open(sys.argv[1], encoding="utf-8") as stream:
        check(json.load(stream), sys.argv[2])
