import copy
import unittest

from check_scan_inventory import check


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.report = {"Results": [
            {"Target": "backend/go.mod", "Packages": [
                {"Name": "github.com/IEZhu/class/backend", "Relationship": "root",
                 "AnalyzedBy": "gomod", "DependsOn": ["stdlib@v1.26.10"]},
                {"Name": "stdlib", "Version": "v1.26.10"}]},
            {"Target": "web/package-lock.json", "Packages": [
                {"Name": "next", "Version": "15.5.28"}]},
        ]}

    def test_real_first_party_source_root_does_not_need_a_fabricated_version(self):
        check(self.report, "source")

    def test_unversioned_third_party_package_is_rejected(self):
        for root in (False, True):
            with self.subTest(root=root):
                report = copy.deepcopy(self.report)
                report["Results"][0]["Packages"][0]["Name"] = "third-party.example/module"
                if not root:
                    report["Results"][0]["Packages"][0].pop("Relationship")
                with self.assertRaises(ValueError):
                    check(report, "source")

    def test_missing_or_empty_manifest_inventory_is_rejected(self):
        for report in ({}, {"Results": []}, {"Results": self.report["Results"][:1]}):
            with self.subTest(report=report), self.assertRaises(ValueError):
                check(report, "source")

    def test_every_vulnerability_and_secret_blocks(self):
        for key in ("Vulnerabilities", "Secrets"):
            with self.subTest(key=key):
                report = copy.deepcopy(self.report)
                report["Results"][0][key] = [{"Severity": "UNKNOWN"}]
                with self.assertRaisesRegex(ValueError, "findings"):
                    check(report, "source")

    def test_api_requires_both_os_and_compiled_go_inventory(self):
        packages = [{"Name": "alpine-baselayout", "Version": "3.7"}]
        report = {"Results": [{"Class": "os-pkgs", "Packages": packages}]}
        with self.assertRaises(ValueError):
            check(report, "api")
        report["Results"].append({"Packages": [{"Name": "stdlib", "Version": "v1.26.10"}]})
        check(report, "api")
        report["Results"][0].pop("Class")
        with self.assertRaises(ValueError):
            check(report, "api")

    def test_web_requires_the_runtime_and_application_packages(self):
        report = {"Results": [{"Packages": [
            {"Name": "nodejs", "Version": "22"},
            {"Name": "next", "Version": "15.5.28"},
            {"Name": "react", "Version": "19.2.8"},
        ]}]}
        check(report, "web")
        report["Results"][0]["Packages"].pop()
        with self.assertRaises(ValueError):
            check(report, "web")


if __name__ == "__main__":
    unittest.main()
