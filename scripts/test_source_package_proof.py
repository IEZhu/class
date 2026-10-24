import json
from pathlib import Path
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone

sys.path.insert(0, str(Path(__file__).resolve().parent / "security"))
import source_package_proof as proof


class SourceProofTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "backend"
        self.root.mkdir()
        for name in ("go.mod", "go.sum", "main.go"):
            (self.root / name).write_text(name)
        self.output = Path(self.temp.name) / "proof"
        self.output.mkdir()
        self.module = "golang.org/x/crypto"
        self.purl = "pkg:golang/" + self.module + "@v0.58.0"
        self.report = {"Results": [{
            "Target": "backend/go.mod", "Type": "gomod", "Packages": [
                {"Name": proof.MODULE, "Relationship": "root", "AnalyzedBy": "gomod", "DependsOn": [self.module],
                 "Identifier": {"PURL": "pkg:golang/" + proof.MODULE.lower()}},
                {"Name": self.module, "Version": "v0.58.0", "Identifier": {"PURL": self.purl}},
            ], "Vulnerabilities": [{"VulnerabilityID": "GO-test", "PkgIdentifier": {"PURL": self.purl}}],
        }, {"Target": "web/package-lock.json", "Packages": [{"Name": "next", "Version": "15.5.28"}]}]}
        self.messages = [{"config": {
            "scanner_name": "govulncheck", "scanner_version": "v1.9.0",
            "scan_level": "package", "scan_mode": "source", "db": "https://vuln.go.dev", "go_version": proof.GO_VERSION,
        }}, {"SBOM": {"roots": [proof.MODULE + "/internal/httpapi.test"], "modules": [
            {"path": proof.MODULE}, {"path": self.module, "version": "v0.58.0"},
        ]}}]
        self.graph = [{"ImportPath": proof.MODULE + "/internal/httpapi.test", "Module": {"Path": proof.MODULE}}]
        self.vex = {"tooling": proof.TOOL, "statements": [{
            "vulnerability": {"name": "GO-test"}, "status": "not_affected",
            "justification": "vulnerable_code_not_present",
            "products": [{"subcomponents": [{"@id": self.purl}]}],
        }]}

    def write(self):
        (self.output / "events.json").write_text("\n".join(json.dumps(m) for m in self.messages))
        (self.output / "packages.json").write_text("\n".join(json.dumps(m) for m in self.graph))
        (self.output / "vex.json").write_text(json.dumps(self.vex))
        (self.output / "gate.txt").write_text("No vulnerabilities found.\n")
        (self.output / "modules.txt").write_text("all modules verified\n")
        manifest = {
            "created_at": datetime.now(timezone.utc).isoformat(), "source_files": proof.snapshot(self.root),
            "environment": proof.ENVIRONMENT,
            "report_sha256": proof.sha(json.dumps(self.report, sort_keys=True).encode()),
            "outputs": {p.name: proof.sha(p.read_bytes()) for p in self.output.iterdir() if p.name != "manifest.json"},
        }
        (self.output / "manifest.json").write_text(json.dumps(manifest))

    def test_exact_source_absence_can_pass_without_mutating_raw_report(self):
        self.write()
        original = json.dumps(self.report)
        allowed = proof.validate(self.report, self.root, self.output)
        proof.check(self.report, "source", allowed)
        self.assertEqual(original, json.dumps(self.report))
        with self.assertRaisesRegex(ValueError, "findings"):
            proof.check(self.report, "source")

    def test_changed_source_report_output_or_platform_blocks(self):
        for mutation in ("source", "report", "output", "platform", "stale"):
            with self.subTest(mutation=mutation):
                self.write()
                manifest_path = self.output / "manifest.json"
                manifest = json.loads(manifest_path.read_text())
                if mutation == "source": manifest["source_files"]["main.go"] = "another"
                if mutation == "report": manifest["report_sha256"] = "another"
                if mutation == "output": (self.output / "gate.txt").write_text("another")
                if mutation == "platform": manifest["environment"] = dict(proof.ENVIRONMENT, GOOS="windows")
                if mutation == "stale": manifest["created_at"] = (datetime.now(timezone.utc) - timedelta(days=1)).isoformat()
                manifest_path.write_text(json.dumps(manifest))
                with self.assertRaises(ValueError): proof.validate(self.report, self.root, self.output)

    def test_affected_packages_empty_graph_wrong_versions_and_weak_proofs_block(self):
        for mutation in ("affected", "unreachable", "present", "empty", "replace", "version", "symbol", "test", "malformed"):
            with self.subTest(mutation=mutation):
                messages = json.loads(json.dumps(self.messages))
                graph = json.loads(json.dumps(self.graph))
                vex = json.loads(json.dumps(self.vex))
                if mutation == "affected": vex["statements"][0]["status"] = "affected"
                if mutation == "unreachable": vex["statements"][0]["justification"] = "vulnerable_code_not_in_execute_path"
                if mutation == "present": messages.append({"finding": {"trace": [{"package": self.module + "/openpgp"}]}})
                if mutation == "empty": graph.clear()
                if mutation == "replace": graph[0]["Module"]["Replace"] = {"Dir": "somewhere"}
                if mutation == "version": messages[1]["SBOM"]["modules"][1]["version"] = "v99.0.0"
                if mutation == "symbol": messages[0]["config"]["scan_level"] = "symbol"
                if mutation == "test": graph[0]["ImportPath"] = proof.MODULE + "/main"
                if mutation == "malformed": messages.append({"finding": {}})
                original = self.messages, self.graph, self.vex
                self.messages, self.graph, self.vex = messages, graph, vex
                self.write()
                with self.assertRaises(ValueError): proof.validate(self.report, self.root, self.output)
                self.messages, self.graph, self.vex = original

    def test_unproven_npm_finding_and_secrets_always_block(self):
        self.write()
        allowed = proof.validate(self.report, self.root, self.output)
        for key in ("Vulnerabilities", "Secrets"):
            with self.subTest(key=key):
                report = json.loads(json.dumps(self.report))
                report["Results"][1][key] = [{"VulnerabilityID": "unproven"}]
                with self.assertRaisesRegex(ValueError, "findings"):
                    proof.check(report, "source", allowed)


if __name__ == "__main__":
    unittest.main()
