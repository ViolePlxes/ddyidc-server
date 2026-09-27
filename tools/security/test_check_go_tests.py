import json
import unittest
from check_go_tests import summarize


class GoTestEvidenceTests(unittest.TestCase):
    def events(self, *values):
        return [json.dumps(value) for value in values]

    def test_unregistered_skip_fails(self):
        report = summarize(self.events({"Package": "p", "Action": "start"},
                                      {"Package": "p", "Test": "TestDB", "Action": "skip"},
                                      {"Package": "p", "Action": "pass"}), {})
        self.assertFalse(report["gatePassed"])
        self.assertEqual(report["passedTests"], 0)

    def test_registered_skip_stays_separate(self):
        report = summarize(self.events({"Package": "p", "Action": "start"},
                                      {"Package": "p", "Test": "TestDB", "Action": "skip"},
                                      {"Package": "p", "Action": "pass"}),
                           {"p/TestDB": {"disposition": "isolated_suite", "verification": "separate database run"}})
        self.assertTrue(report["gatePassed"])
        self.assertEqual(report["skipped"][0]["result"], "skipped_not_passed")

    def test_interrupted_package_fails(self):
        report = summarize(self.events({"Package": "p", "Action": "start"}), {})
        self.assertFalse(report["gatePassed"])

    def test_empty_report_fails(self):
        self.assertFalse(summarize([], {})["gatePassed"])
