"""Audit go test -json evidence without treating skipped integration tests as passes."""
import argparse
import json
from pathlib import Path


def summarize(lines, registered):
    packages, completed, passed, failed, skipped, no_tests = set(), set(), [], [], [], []
    for line in lines:
        event = json.loads(line)
        package, test, action = event.get("Package"), event.get("Test"), event.get("Action")
        if action == "start":
            packages.add(package)
        if action in ("pass", "fail", "skip") and not test:
            completed.add(package)
        name = f"{package}/{test}" if test else package
        if action == "fail":
            failed.append(name)
        elif action == "pass" and test:
            passed.append(name)
        elif action == "skip" and test:
            item = {"test": name, "result": "skipped_not_passed"}
            item.update(registered.get(name, {"disposition": "UNREGISTERED"}))
            skipped.append(item)
        elif action == "skip":
            no_tests.append(package)
    unknown = [s["test"] for s in skipped if s["disposition"] in ("UNREGISTERED", "legacy_skip_replaced")]
    unfinished = sorted(packages - completed)
    return {"passedTests": len(passed), "failed": failed, "skipped": skipped,
            "packagesWithoutTests": len(no_tests), "unfinishedPackages": unfinished,
            "unexpectedSkips": unknown, "gatePassed": bool(packages) and not (failed or unfinished or unknown)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("evidence", type=Path)
    parser.add_argument("registry", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    registry = json.loads(args.registry.read_text(encoding="utf-8"))
    for name, value in registry.items():
        if not value.get("disposition") or not value.get("verification"):
            raise SystemExit(f"Incomplete skip registration: {name}")
    with args.evidence.open(encoding="utf-8") as source:
        result = summarize(source, registry)
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"passed={result['passedTests']} skipped_not_passed={len(result['skipped'])} failed={len(result['failed'])}")
    raise SystemExit(0 if result["gatePassed"] else 1)


if __name__ == "__main__":
    main()
