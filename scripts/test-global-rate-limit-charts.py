#!/usr/bin/env python3
"""Verify global quota environment values without printing rendered secrets."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parent.parent


class GlobalRateLimitCharts(unittest.TestCase):
    def test_global_limits(self):
        for settings, expected in (
            ([], ("60", "100000")),
            (["gateway.globalRateLimits.rpm=0", "gateway.globalRateLimits.tpm=0"], ("0", "0")),
            (["gateway.globalRateLimits.rpm=120", "gateway.globalRateLimits.tpm=200000"], ("120", "200000")),
            (["gateway.globalRateLimits=null"], ("60", "100000")),
        ):
            with self.subTest(settings=settings):
                args = ["helm", "template", "quota-check", str(ROOT / "charts/ai-gateway")]
                for value in settings:
                    args += ["--set", value]
                result = subprocess.run(args, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, "Helm rendering failed")
                values = dict(re.findall(r'name: (GLOBAL_RATE_LIMIT_[A-Z]+)\s+value: "([0-9]+)"', result.stdout))
                self.assertEqual((values.get("GLOBAL_RATE_LIMIT_RPM"), values.get("GLOBAL_RATE_LIMIT_TPM")), expected)


if __name__ == "__main__":
    unittest.main()
