#!/usr/bin/env python3
"""Keep secret findings blocking while removing their values from CI artifacts."""

import json
import sys
from pathlib import Path


def redact(report: dict) -> dict:
    for result in report.get("Results", []):
        if result.get("Secrets"):
            result["Secrets"] = [
                {
                    key: value
                    for key, value in finding.items()
                    if key
                    in {
                        "RuleID",
                        "Category",
                        "Severity",
                        "Title",
                        "StartLine",
                        "EndLine",
                    }
                }
                for finding in result["Secrets"]
            ]
    return report


if __name__ == "__main__":
    for argument in sys.argv[1:]:
        path = Path(argument)
        if path.is_file():
            path.write_text(json.dumps(redact(json.loads(path.read_text())), indent=2))
