import json
import unittest
from check_govulncheck import summarize


class ScannerGateTest(unittest.TestCase):
    def test_symbol_finding_cannot_be_hidden_by_module_finding(self):
        items = [{"config": {"scanner_name": "govulncheck"}},
                 {"finding": {"osv": "TEST-1", "trace": [{"module": "test"}]}},
                 {"finding": {"osv": "TEST-1", "trace": [{"module": "test", "function": "Danger"}]}}]
        report = summarize("\n".join(json.dumps(item) for item in items))
        self.assertEqual(report["affected_symbol_advisories"], 1)

    def test_empty_and_truncated_reports_are_not_passes(self):
        for text in ["", '{"config":']:
            with self.assertRaises(ValueError):
                summarize(text)

    def test_module_only_is_reported_separately(self):
        report = summarize('{"config":{}} {"finding":{"osv":"TEST-2","trace":[{"module":"unused"}]}}')
        self.assertEqual(report["affected_symbol_advisories"], 0)
        self.assertEqual(len(report["findings"]), 1)


if __name__ == "__main__":
    unittest.main()
