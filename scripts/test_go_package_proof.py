import copy
from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent / "security"))
import go_package_proof as proof


class BinaryProofTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.target = "usr/bin/service"
        self.prefix = self.directory / proof.key(self.target)
        self.prefix.with_suffix(".bin").write_bytes(b"fixture binary")
        self.module = "golang.org/x/crypto"
        self.purl = "pkg:golang/" + self.module + "@v0.58.0"
        self.report = {
            "Metadata": {"ImageID": "sha256:" + "a" * 64,
                         "RepoDigests": ["localhost:5000/test@sha256:" + "b" * 64],
                         "ImageConfig": {"architecture": "amd64"}},
            "Results": [{"Target": self.target, "Type": "gobinary", "Packages": [
                {"Name": self.module, "Version": "v0.58.0", "Identifier": {"PURL": self.purl}},
            ], "Vulnerabilities": [{"VulnerabilityID": "GO-test", "PkgIdentifier": {"PURL": self.purl}}]}],
        }
        self.messages = [{"config": {"scanner_name": "govulncheck", "scanner_version": "v1.9.0",
            "scan_level": "package", "scan_mode": "binary", "db": "https://vuln.go.dev"}},
            {"SBOM": {"modules": [{"path": self.module, "version": "v0.58.0"}]}}]
        self.vex = {"tooling": proof.TOOL, "statements": [{
            "vulnerability": {"name": "GO-test"}, "status": "not_affected", "justification": "vulnerable_code_not_present",
            "products": [{"@id": "Unknown Product", "subcomponents": [{"@id": self.purl}]}],
        }]}
        self.extracted = [{"name": "govulncheck-extract", "version": "0.1.0"},
                          {"pkgSymbols": [{"name": "main", "pkg": "main"}], "goos": "linux", "goarch": "amd64"}]
        self.manifest = {"created_at": datetime.now(timezone.utc).isoformat(),
            "image_id": self.report["Metadata"]["ImageID"], "repo_digest": self.report["Metadata"]["RepoDigests"][0],
            "report_sha256": proof.hashlib.sha256(json.dumps(self.report, sort_keys=True).encode()).hexdigest(),
            "binaries": {self.target: proof.digest(self.prefix.with_suffix(".bin"))}}
        self.write()

    def write(self):
        self.prefix.with_suffix(".events.json").write_text("\n".join(json.dumps(x) for x in self.messages))
        self.prefix.with_suffix(".extract.json").write_text("\n".join(json.dumps(x) for x in self.extracted))
        self.prefix.with_suffix(".vex.json").write_text(json.dumps(self.vex))

    def test_package_absence_binds_exact_image_and_preserves_inventory(self):
        document = proof.bind(self.report, self.manifest, self.directory)
        self.assertIn("@sha256:", document["statements"][0]["products"][0]["@id"])
        final = copy.deepcopy(self.report)
        final["Results"][0]["Vulnerabilities"] = []
        proof.compare(self.report, final, document)
        final["Results"][0]["Packages"] = []
        with self.assertRaisesRegex(ValueError, "inventory"): proof.compare(self.report, final, document)

    def test_stale_or_mismatched_artifacts_block(self):
        for mutation in ("image", "digest", "stale", "binary", "coverage", "report"):
            with self.subTest(mutation=mutation):
                manifest = copy.deepcopy(self.manifest)
                if mutation == "image": manifest["image_id"] = "sha256:" + "c" * 64
                if mutation == "digest": manifest["repo_digest"] = "localhost:5000/test@sha256:" + "c" * 64
                if mutation == "stale": manifest["created_at"] = (datetime.now(timezone.utc) - timedelta(days=1)).isoformat()
                if mutation == "binary": manifest["binaries"][self.target] = "another"
                if mutation == "coverage": manifest["binaries"] = {}
                if mutation == "report": manifest["report_sha256"] = "another"
                with self.assertRaises(ValueError): proof.bind(self.report, manifest, self.directory)

    def test_affected_packages_weaker_scans_missing_symbols_and_modules_block(self):
        for mutation in ("symbol", "version", "modules", "present", "affected", "unreachable", "stripped"):
            with self.subTest(mutation=mutation):
                original = copy.deepcopy((self.messages, self.vex, self.extracted))
                if mutation == "symbol": self.messages[0]["config"]["scan_level"] = "symbol"
                if mutation == "version": self.messages[0]["config"]["scanner_version"] = "other"
                if mutation == "modules": self.messages[1]["SBOM"]["modules"] = []
                if mutation == "present": self.messages.append({"finding": {"trace": [{"package": self.module + "/openpgp"}]}})
                if mutation == "affected": self.vex["statements"][0]["status"] = "affected"
                if mutation == "unreachable": self.vex["statements"][0]["justification"] = "vulnerable_code_not_in_execute_path"
                if mutation == "stripped": self.extracted = [{}, {}]
                self.write()
                with self.assertRaises(ValueError): proof.bind(self.report, self.manifest, self.directory)
                self.messages, self.vex, self.extracted = original

    def test_unproven_findings_cannot_be_cleared(self):
        document = proof.bind(self.report, self.manifest, self.directory)
        raw = copy.deepcopy(self.report)
        raw["Results"][0]["Vulnerabilities"].append({"VulnerabilityID": "unproven", "PkgIdentifier": {"PURL": "pkg:golang/other@v1"}})
        final = copy.deepcopy(raw)
        final["Results"][0]["Vulnerabilities"] = []
        with self.assertRaisesRegex(ValueError, "without exact"): proof.compare(raw, final, document)

    def test_third_party_root_version_or_purl_loss_always_blocks(self):
        for version, purl in (("", "pkg:golang/github.com/caddyserver/caddy/v2"),
                              ("(devel)", "pkg:golang/github.com/caddyserver/caddy/v2"),
                              ("v2.11.7", "pkg:golang/github.com/caddyserver/caddy/v2@v2.10.0")):
            with self.subTest(version=version, purl=purl):
                report = copy.deepcopy(self.report)
                report["Results"][0]["Packages"].append({"Name": "github.com/caddyserver/caddy/v2", "Version": version,
                    "Relationship": "root", "AnalyzedBy": "gobinary", "DependsOn": [self.module], "Identifier": {"PURL": purl}})
                with self.assertRaises(ValueError): proof.go_results(report)

    def test_only_the_exact_observed_first_party_binary_root_can_be_unversioned(self):
        root = {"Name": proof.FIRST_PARTY_MODULE, "Relationship": "root", "AnalyzedBy": "gobinary",
                "DependsOn": [self.module + "@v0.58.0"], "Identifier": {"PURL": "pkg:golang/" + proof.FIRST_PARTY_MODULE.lower()}}
        self.report["Results"][0]["Packages"].append(root)
        self.messages[1]["SBOM"]["modules"].append({"path": proof.FIRST_PARTY_MODULE, "version": "(devel)"})
        self.manifest["report_sha256"] = proof.hashlib.sha256(json.dumps(self.report, sort_keys=True).encode()).hexdigest()
        self.write()
        proof.bind(self.report, self.manifest, self.directory)
        for field in ("Relationship", "AnalyzedBy", "DependsOn", "Identifier"):
            with self.subTest(field=field):
                report = copy.deepcopy(self.report)
                report["Results"][0]["Packages"][-1].pop(field)
                with self.assertRaises(ValueError): proof.go_results(report)


if __name__ == "__main__":
    unittest.main()
