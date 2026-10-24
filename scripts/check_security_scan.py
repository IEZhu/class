"""Require complete inventories as well as zero fixed HIGH/CRITICAL findings."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import tarfile


def pair(name, version):
    if not isinstance(name, str) or not name or not isinstance(version, str) or version.lower() in ("", "unknown", "devel", "(devel)"):
        raise ValueError("missing package name/version")
    return name, version.removeprefix("v")


def source_inventory(root):
    lock = json.loads((root / "web/package-lock.json").read_text())
    npm = set()
    for path, package in lock["packages"].items():
        if not path:
            continue
        if package.get("link"):
            raise ValueError("linked npm packages need an explicit inventory implementation")
        npm.add(pair(package.get("name", path.rsplit("node_modules/", 1)[-1]), package.get("version")))
    mod = (root / "backend/go.mod").read_text()
    if re.search(r"(?m)^\s*(replace|exclude)\b", mod):
        raise ValueError("Go replacements/exclusions need an explicit inventory implementation")
    modules = {pair(name, version) for name, version in re.findall(r"(?m)^\s*(?:require\s+)?(\S+)\s+(v\S+)(?:\s+//.*)?$", mod)}
    module = re.search(r"(?m)^module\s+(\S+)$", mod).group(1)
    if not npm or not modules:
        raise ValueError("empty expected dependency inventory")
    return {"groups": {"web/package-lock.json": sorted(npm), "backend/go.mod": sorted(modules)}, "roots": [module]}


def image_inventory(service, image, destination):
    """Read the built filesystem and Go build info without starting the API."""
    destination.mkdir(parents=True, exist_ok=True)
    container = subprocess.check_output(["docker", "create", image], text=True).strip()
    archive = destination / "filesystem.tar"
    try:
        with archive.open("wb") as output:
            subprocess.run(["docker", "export", container], stdout=output, check=True)
    finally:
        subprocess.run(["docker", "rm", container], check=True)
    os_packages, libraries, roots = set(), set(), []
    with tarfile.open(archive) as filesystem:
        members = {m.name.lstrip("./"): m for m in filesystem.getmembers() if m.isfile()}
        database = filesystem.extractfile(members["lib/apk/db/installed"]).read().decode()
        for stanza in database.split("\n\n"):
            fields = dict(line.split(":", 1) for line in stanza.splitlines() if ":" in line)
            if fields:
                os_packages.add(pair(fields.get("P"), fields.get("V")))
        if service == "web":
            for name, member in members.items():
                if "/node_modules/" in "/" + name and name.endswith("/package.json"):
                    package = json.load(filesystem.extractfile(member))
                    if package.get("name") and package.get("version"):
                        libraries.add(pair(package["name"], package["version"]))
            node = subprocess.check_output(["docker", "run", "--rm", "--network", "none", "--entrypoint", "node", image, "--version"], text=True).strip().removeprefix("v")
            if not any(name == "nodejs" and version.startswith(node + "-r") for name, version in os_packages):
                raise ValueError("running Node version is not covered by the installed APK inventory")
        elif service == "backend":
            binary = destination / "api"
            binary.write_bytes(filesystem.extractfile(members["usr/local/bin/api"]).read())
            info = subprocess.check_output(["go", "version", "-m", str(binary)], text=True)
            (destination / "go-build-info.txt").write_text(info)
            libraries.add(pair("stdlib", re.search(r": go(\S+)", info).group(1)))
            for line in info.splitlines():
                fields = line.split()
                if fields and fields[0] == "dep":
                    libraries.add(pair(fields[1], fields[2]))
                elif fields and fields[0] == "mod":
                    if fields[2] != "(devel)":
                        libraries.add(pair(fields[1], fields[2]))
                    else:
                        roots.append(fields[1])
                elif fields and fields[0] == "=>":
                    raise ValueError("Go binary replacements need explicit inventory handling")
        else:
            raise ValueError("unknown runtime service")
    archive.unlink()
    if not os_packages or not libraries:
        raise ValueError("empty expected runtime inventory")
    return {"groups": {"os": sorted(os_packages), "libraries": sorted(libraries)}, "roots": roots}


def check(report, expected, stderr):
    if re.search(r"err\.message=|\b(?:ERROR|FATAL)\b", stderr):
        raise ValueError("scanner reported an error or incomplete advisory details")
    results = report.get("Results")
    if report.get("SchemaVersion") != 2 or not isinstance(results, list) or not results:
        raise ValueError("missing or unsupported scanner results")
    groups = expected["groups"]
    if not groups or any(not values for values in groups.values()):
        raise ValueError("empty expected inventory")
    seen = {name: set() for name in groups}
    for result in results:
        if result.get("Vulnerabilities"):
            raise ValueError("fixed HIGH/CRITICAL vulnerabilities must block the check")
        packages = result.get("Packages", [])
        for package in packages:
            if package.get("Name") in expected["roots"] and package.get("Version", "") in ("", "(devel)"):
                continue
            entry = pair(package.get("Name"), package.get("Version"))
            target = result.get("Target", "").removeprefix("/src/")
            if target in seen:
                seen[target].add(entry)
            category = "os" if result.get("Class") == "os-pkgs" else "libraries"
            if category in seen:
                seen[category].add(entry)
    for group, values in groups.items():
        missing = set(map(tuple, values)) - seen[group]
        if missing:
            raise ValueError(f"incomplete {group} inventory: {sorted(missing)}")
    print("Verified inventories: " + ", ".join(f"{name}={len(values)}" for name, values in groups.items()))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    source = commands.add_parser("source-inventory")
    source.add_argument("root", type=Path)
    source.add_argument("output", type=Path)
    image = commands.add_parser("image-inventory")
    image.add_argument("service", choices=("web", "backend"))
    image.add_argument("image")
    image.add_argument("directory", type=Path)
    verify = commands.add_parser("check")
    verify.add_argument("report", type=Path)
    verify.add_argument("expected", type=Path)
    verify.add_argument("stderr", type=Path)
    args = parser.parse_args()
    if args.command == "source-inventory":
        args.output.write_text(json.dumps(source_inventory(args.root), indent=2) + "\n")
    elif args.command == "image-inventory":
        expected = image_inventory(args.service, args.image, args.directory)
        (args.directory / "expected.json").write_text(json.dumps(expected, indent=2) + "\n")
    else:
        check(json.loads(args.report.read_text()), json.loads(args.expected.read_text()), args.stderr.read_text())


if __name__ == "__main__":
    main()
