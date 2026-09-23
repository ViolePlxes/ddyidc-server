"""Summarize govulncheck JSON; fail on affected symbols, not JSON exit codes."""
import json
import pathlib
import sys


def summarize(text):
    decoder = json.JSONDecoder()
    offset = 0
    config = None
    findings = {}
    while offset < len(text):
        if text[offset].isspace():
            offset += 1
            continue
        item, offset = decoder.raw_decode(text, offset)
        if "config" in item:
            config = item["config"]
        finding = item.get("finding")
        if not finding:
            continue
        trace = finding.get("trace", [])
        first = trace[0] if trace else {}
        key = (finding["osv"], first.get("module", ""))
        record = findings.setdefault(key, {"id": key[0], "module": key[1],
                                          "fixed_version": finding.get("fixed_version", ""),
                                          "symbol_affected": False})
        record["symbol_affected"] |= bool(first.get("function"))
    if config is None:
        raise ValueError("missing scanner config: empty or incomplete evidence")
    return {"scanner": config, "findings": list(findings.values()),
            "affected_symbol_advisories": sum(row["symbol_affected"] for row in findings.values())}


if __name__ == "__main__":
    try:
        report = summarize(pathlib.Path(sys.argv[1]).read_text())
        print(json.dumps(report, ensure_ascii=False, indent=2))
        sys.exit(1 if report["affected_symbol_advisories"] else 0)
    except (ValueError, OSError, IndexError) as error:
        print("invalid vulnerability evidence: " + str(error), file=sys.stderr)
        sys.exit(2)
