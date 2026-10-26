from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent / "security"))
from check_scanner_errors import check
from redact_report import redact
from check_scan_inventory import check as inventory


class ScannerBoundaryTests(unittest.TestCase):
    def test_nonfatal_advisory_database_error_cannot_pass(self):
        with self.assertRaises(ValueError):
            check('WARN\tUnable to get vulnerability details\terr.message="failed to get vulnerability"')
        with self.assertRaises(ValueError):
            check('time\tERROR\tscanner failure')
        check('time\tWARN\tUsing severities from other vendors for some vulnerabilities')

    def test_redacted_secret_still_blocks_without_disclosing_its_value(self):
        report = {"Results": [{"Target": "web", "Packages": [
            {"Name": name, "Version": "1.0.0"} for name in ("nodejs", "next", "react")
        ], "Secrets": [{"RuleID": "github-pat", "Severity": "HIGH", "Match": "unissued fixture value"}]}]}
        report["Results"].append({"Class": "os-pkgs", "Packages": [
            {"Name": "alpine-baselayout", "Version": "3.7"}
        ]})
        redact(report)
        self.assertNotIn("Match", report["Results"][0]["Secrets"][0])
        self.assertEqual(report["Results"][0]["Secrets"][0]["RuleID"], "github-pat")
        with self.assertRaisesRegex(ValueError, "findings"):
            inventory(report, "web")


if __name__ == "__main__":
    unittest.main()
