import copy
import unittest

from check_scan_inventory import check


class InventoryTests(unittest.TestCase):
    def setUp(self):
        self.report = {"Results": [
            {"Target": "backend/go.mod", "Packages": [
                {"Name": "github.com/IEZhu/class/backend", "Relationship": "root",
                 "AnalyzedBy": "gomod", "DependsOn": ["stdlib@v1.26.10"],
                 "Identifier": {"PURL": "pkg:golang/github.com/iezhu/class/backend"}},
                {"Name": "stdlib", "Version": "v1.26.10"}]},
            {"Target": "web/package-lock.json", "Packages": [
                {"Name": "next", "Version": "15.5.28"}]},
        ]}

    def test_real_first_party_source_root_does_not_need_a_fabricated_version(self):
        check(self.report, "source")

    def test_unversioned_third_party_package_is_rejected(self):
        for root, version in ((False, ""), (True, ""), (True, "(devel)"), (True, "unknown")):
            with self.subTest(root=root, version=version):
                report = copy.deepcopy(self.report)
                report["Results"][0]["Packages"][0]["Name"] = "third-party.example/module"
                report["Results"][0]["Packages"][0]["Version"] = version
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

    def test_go_runtimes_require_both_os_and_compiled_go_inventory(self):
        packages = [{"Name": "alpine-baselayout", "Version": "3.7"}]
        report = {"Results": [{"Class": "os-pkgs", "Packages": packages}]}
        for target in ("api", "caddy"):
            with self.subTest(target=target), self.assertRaises(ValueError):
                check(report, target)
        report["Results"].append({"Type": "gobinary", "Packages": [
            {"Name": "stdlib", "Version": "v1.26.10"},
        ]})
        for target in ("api", "caddy"):
            with self.subTest(target=target):
                check(report, target)
        report["Results"][0].pop("Class")
        for target in ("api", "caddy"):
            with self.subTest(target=target), self.assertRaises(ValueError):
                check(report, target)

    def test_go_runtimes_reject_stdlib_outside_compiled_go_inventory(self):
        for target in ("api", "caddy"):
            for result_type in (None, "alpine", "gomod"):
                with self.subTest(target=target, result_type=result_type):
                    report = {"Results": [{"Class": "os-pkgs", "Packages": [
                        {"Name": "alpine-baselayout", "Version": "3.7"},
                    ]}, {"Packages": [{"Name": "stdlib", "Version": "v1.26.10"}]}]}
                    if result_type is not None:
                        report["Results"][1]["Type"] = result_type
                    with self.assertRaisesRegex(ValueError, "compiled Go runtime"):
                        check(report, target)

    def test_web_requires_the_runtime_and_application_packages(self):
        report = {"Results": [{"Class": "os-pkgs", "Packages": [
            {"Name": "alpine-baselayout", "Version": "3.7"},
        ]}, {"Packages": [
            {"Name": "nodejs", "Version": "22"},
            {"Name": "next", "Version": "15.5.28"},
            {"Name": "react", "Version": "19.2.8"},
        ]}]}
        check(report, "web")
        report["Results"][1]["Packages"].pop()
        with self.assertRaises(ValueError):
            check(report, "web")

    def test_web_rejects_missing_or_empty_os_inventory(self):
        report = {"Results": [{"Packages": [
            {"Name": "nodejs", "Version": "22"},
            {"Name": "next", "Version": "15.5.28"},
            {"Name": "react", "Version": "19.2.8"},
        ]}]}
        for os_result in (None, {"Class": "os-pkgs", "Packages": []}):
            with self.subTest(os_result=os_result):
                candidate = copy.deepcopy(report)
                if os_result is not None:
                    candidate["Results"].append(os_result)
                with self.assertRaisesRegex(ValueError, "OS inventory"):
                    check(candidate, "web")


if __name__ == "__main__":
    unittest.main()
