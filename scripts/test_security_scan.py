"""Regression controls for omitted packages, findings and partial scans."""

import copy
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
from unittest import mock

from check_security_scan import check, image_inventory, source_inventory


class ScanGateTests(unittest.TestCase):
    def setUp(self):
        self.expected = {"groups": {
            "web/package-lock.json": [["next", "15.5.28"], ["typescript", "5.9.3"]],
            "backend/go.mod": [["example.org/direct", "1.0.0"], ["example.org/transitive", "2.0.0"]],
        }, "roots": ["example.org/app"]}
        self.report = {"SchemaVersion": 2, "Results": [
            {"Target": name, "Class": "lang-pkgs", "Packages": [
                {"Name": p, "Version": v} for p, v in packages
            ]} for name, packages in self.expected["groups"].items()
        ]}

    def test_complete_source_inventory(self):
        check(self.report, self.expected, "INFO vulnerability scanning enabled")

    def test_missing_dev_or_transitive_package_fails(self):
        for index in (0, 1):
            with self.subTest(index=index), self.assertRaisesRegex(ValueError, "incomplete"):
                report = copy.deepcopy(self.report)
                report["Results"][index]["Packages"].pop()
                check(report, self.expected, "")

    def test_missing_or_malformed_results_fail(self):
        for report in ({}, {"SchemaVersion": 2, "Results": []}, {"SchemaVersion": 2, "Results": [{}]}):
            with self.subTest(report=report), self.assertRaises(ValueError):
                check(report, self.expected, "")

    def test_unknown_versions_fail(self):
        self.report["Results"][0]["Packages"][0].pop("Version")
        with self.assertRaisesRegex(ValueError, "name/version"):
            check(self.report, self.expected, "")

    def test_wrong_manifest_cannot_supply_expected_packages(self):
        self.report["Results"][0]["Target"] = "other/package-lock.json"
        with self.assertRaisesRegex(ValueError, "incomplete"):
            check(self.report, self.expected, "")

    def test_vulnerability_fails_even_if_scanner_returned_success(self):
        self.report["Results"][0]["Vulnerabilities"] = [{"VulnerabilityID": "CVE-control", "Severity": "HIGH", "FixedVersion": "1"}]
        with self.assertRaisesRegex(ValueError, "vulnerabilities"):
            check(self.report, self.expected, "")

    def test_logged_errors_fail_including_warn_level_errors(self):
        for stderr in ("\tERROR\trequest failed", "\tFATAL\tfailed", 'WARN err.message="failed to fetch advisory"'):
            with self.subTest(stderr=stderr), self.assertRaisesRegex(ValueError, "scanner reported"):
                check(self.report, self.expected, stderr)

    def test_vendor_severity_notice_is_not_a_scanner_error(self):
        check(self.report, self.expected, "WARN Using severities from other vendors for some vulnerabilities")

    def test_complete_runtime_and_every_missing_package_fail(self):
        expected = {"groups": {"os": [["musl", "1.2.5-r1"]], "libraries": [["stdlib", "1.26.10"], ["example.org/lib", "1.0.0"]]}, "roots": []}
        report = {"SchemaVersion": 2, "Results": [
            {"Class": "os-pkgs", "Packages": [{"Name": "musl", "Version": "1.2.5-r1"}]},
            {"Class": "lang-pkgs", "Packages": [{"Name": "stdlib", "Version": "v1.26.10"}, {"Name": "example.org/lib", "Version": "v1.0.0"}]},
        ]}
        check(report, expected, "")
        for result_index, package_index in ((0, 0), (1, 0), (1, 1)):
            damaged = copy.deepcopy(report)
            damaged["Results"][result_index]["Packages"].pop(package_index)
            with self.subTest(result=result_index, package=package_index), self.assertRaisesRegex(ValueError, "incomplete"):
                check(damaged, expected, "")

    def test_source_expectations_include_all_locked_versions(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "web").mkdir()
            (root / "backend").mkdir()
            (root / "web/package-lock.json").write_text(json.dumps({"packages": {
                "": {"name": "app", "version": "0.0.0"},
                "node_modules/next": {"version": "15.5.28"},
                "node_modules/typescript": {"version": "5.9.3", "dev": True},
                "node_modules/next/node_modules/postcss": {"version": "8.5.18"},
            }}))
            (root / "backend/go.mod").write_text("module example.org/app\n\ngo 1.26.0\n\nrequire (\n example.org/direct v1.0.0\n example.org/transitive v2.0.0 // indirect\n)\n")
            expected = source_inventory(root)
            self.assertEqual(expected["groups"]["backend/go.mod"], [("example.org/direct", "1.0.0"), ("example.org/transitive", "2.0.0")])
            self.assertIn(("typescript", "5.9.3"), expected["groups"]["web/package-lock.json"])
            self.assertIn(("postcss", "8.5.18"), expected["groups"]["web/package-lock.json"])

    def test_shell_wrapper_rejects_scanner_errors_empty_reports_and_findings(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "scripts").mkdir()
            (root / "web").mkdir()
            (root / "backend").mkdir()
            (root / "bin").mkdir()
            for name in ("security_scan.sh", "check_security_scan.py"):
                shutil.copy(Path(__file__).parent / name, root / "scripts" / name)
            (root / "web/package-lock.json").write_text(json.dumps({"packages": {"node_modules/typescript": {"version": "5.9.3", "dev": True}}}))
            (root / "backend/go.mod").write_text("module example.org/app\nrequire example.org/direct v1.0.0\n")
            docker = root / "bin/docker"
            docker.write_text("#!/usr/bin/env python3\nimport json, os, pathlib, sys\nargs=sys.argv[1:]\nassert '--include-dev-deps' in args\nassert args[args.index('--exit-code')+1]=='1'\noutput=next(a.removesuffix(':/evidence') for a in args if a.endswith(':/evidence'))\nif os.environ['CONTROL_REPORT'] != 'absent':\n pathlib.Path(output, 'report.json').write_text(os.environ['CONTROL_REPORT'])\nprint(os.environ.get('CONTROL_STDERR', ''), file=sys.stderr)\nsys.exit(int(os.environ['CONTROL_EXIT']))\n")
            docker.chmod(0o755)
            valid = {"SchemaVersion": 2, "Results": [
                {"Target": "web/package-lock.json", "Packages": [{"Name": "typescript", "Version": "5.9.3"}]},
                {"Target": "backend/go.mod", "Packages": [{"Name": "example.org/direct", "Version": "v1.0.0"}]},
            ]}
            finding = copy.deepcopy(valid)
            finding["Results"][0]["Vulnerabilities"] = [{"Severity": "HIGH", "FixedVersion": "6"}]
            for index, (report, code, stderr, succeeds) in enumerate((
                (json.dumps(valid), 0, "", True),
                (json.dumps(valid), 2, "connection failed", False),
                ("absent", 0, "", False),
                ("{}", 0, "", False),
                ("invalid JSON", 0, "", False),
                (json.dumps(finding), 0, "", False),
                (json.dumps(valid), 0, 'WARN err.message="advisory request failed"', False),
            )):
                env = dict(os.environ, PATH=str(root / "bin") + os.pathsep + os.environ["PATH"], TRIVY_IMAGE="control", CONTROL_REPORT=report, CONTROL_EXIT=str(code), CONTROL_STDERR=stderr)
                if index == 2:
                    (root / f"evidence-{index}").mkdir()
                    (root / f"evidence-{index}" / "report.json").write_text(json.dumps(valid))
                result = subprocess.run(["bash", "scripts/security_scan.sh", "source", str(root / f"evidence-{index}")], cwd=root, env=env, capture_output=True, text=True)
                with self.subTest(index=index):
                    self.assertEqual(result.returncode == 0, succeeds, result.stderr)

    def test_web_image_inventory_requires_the_actual_node_package(self):
        for packaged in (True, False):
            with tempfile.TemporaryDirectory() as temporary:
                archive = io.BytesIO()
                with tarfile.open(fileobj=archive, mode="w") as filesystem:
                    apk = "P:musl\nV:1.2.5-r1\n\n"
                    if packaged:
                        apk += "P:nodejs\nV:22.23.0-r1\n\n"
                    for name, content in {
                        "lib/apk/db/installed": apk,
                        "app/node_modules/next/package.json": '{"name":"next","version":"15.5.28"}',
                        "app/node_modules/next/node_modules/postcss/package.json": '{"name":"postcss","version":"8.5.18"}',
                    }.items():
                        entry = tarfile.TarInfo(name)
                        data = content.encode()
                        entry.size = len(data)
                        filesystem.addfile(entry, io.BytesIO(data))

                def output(command, **kwargs):
                    return "container-id\n" if command[1] == "create" else "v22.23.0\n"

                def run(command, **kwargs):
                    if command[1] == "export":
                        kwargs["stdout"].write(archive.getvalue())
                    return subprocess.CompletedProcess(command, 0)

                with mock.patch("check_security_scan.subprocess.check_output", side_effect=output), mock.patch("check_security_scan.subprocess.run", side_effect=run):
                    if packaged:
                        expected = image_inventory("web", "control-image", Path(temporary))
                        self.assertIn(("nodejs", "22.23.0-r1"), expected["groups"]["os"])
                        self.assertIn(("postcss", "8.5.18"), expected["groups"]["libraries"])
                    else:
                        with self.assertRaisesRegex(ValueError, "Node version is not covered"):
                            image_inventory("web", "control-image", Path(temporary))


if __name__ == "__main__":
    unittest.main()
