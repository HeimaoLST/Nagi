#!/usr/bin/env python3
from __future__ import annotations

import json
import sys


DESKTOP = "github.com/HeimaoLST/Nagi"
CORE = DESKTOP + "/internal/pi"
REQUIRED_GROUPS = {
    "core": (
        CORE,
        (
            "TestPiClientProcessCodingHistoryReopen",
            "TestPiClientProcessIndependentToolAnswerAndCancel",
            "TestPiClientProcessCapturedContextConfiguration",
            "TestPiClientProcessMCPToolsLifecycle",
        ),
    ),
    "product_rpc": (
        CORE,
        (
            "TestPiProductProcessAdmitsCurrentProfileAndRestoresDrafts",
            "TestPiProductProcessTargetsConcurrentActivityQuestions",
            "TestPiProductProcessRetainsUnknownSendWithoutReplay",
        ),
    ),
    "desktop": (
        DESKTOP,
        (
            "TestPiAppConnectionAndConversation",
            "TestPiAppProductConnectionStopAndWorkerLoss",
            "TestPiAppPreviousAndSourceChat",
            "TestPiAppConcurrentActivities",
            "TestPiAppSettingsBackupRestore",
            "TestPiAppRestoresExistingV10Copy",
            "TestPiAppProductScopedDelegation",
            "TestPiAppScopedDomainTools",
            "TestPiAppCrashRecoveryNoReplay",
            "TestPiPackageStandalone",
        ),
    ),
}


def main(path: str) -> int:
    results: dict[tuple[str, str], str] = {}
    failures: set[tuple[str, str]] = set()
    skips: set[tuple[str, str]] = set()
    package_results: dict[str, str] = {}
    package_failures: set[str] = set()
    package_skips: set[str] = set()
    try:
        with open(path, encoding="utf-8") as handle:
            for number, line in enumerate(handle, 1):
                if not line.strip():
                    continue
                event = json.loads(line)
                if not isinstance(event, dict):
                    raise ValueError(f"line {number}: event is not an object")
                action = event.get("Action")
                if action not in ("pass", "fail", "skip"):
                    continue
                package = event.get("Package")
                test = event.get("Test")
                if not isinstance(package, str) or not package:
                    raise ValueError(f"line {number}: terminal event has no package")
                if test is not None and (not isinstance(test, str) or not test):
                    raise ValueError(f"line {number}: invalid test name")
                if test:
                    key = (package, test)
                    results[key] = action
                    if action == "fail":
                        failures.add(key)
                    if action == "skip":
                        skips.add(key)
                else:
                    package_results[package] = action
                    if action == "fail":
                        package_failures.add(package)
                    if action == "skip":
                        package_skips.add(package)
    except (OSError, ValueError) as error:
        print(f"checker: cannot validate {path}: {error}", file=sys.stderr)
        return 1

    errors: list[str] = []
    passes = {key for key, action in results.items() if action == "pass"} - failures - skips
    for group, (package, tests) in REQUIRED_GROUPS.items():
        passed = 0
        for test in tests:
            key = (package, test)
            action = results.get(key, "missing")
            if key in failures:
                action = "fail"
            elif key in skips:
                action = "skip"
            print(f"checker: MUST {package}::{test} = {action}")
            if action == "pass":
                passed += 1
            else:
                errors.append(f"required test {package}::{test}: {action}")
        print(f"checker: {group} required={len(tests)} pass={passed} missing={sum((package, test) not in results for test in tests)}")
        package_action = package_results.get(package, "missing")
        print(f"checker: PACKAGE {package} = {package_action}")
        if package_action != "pass":
            errors.append(f"required package {package}: {package_action}")
    for package, test in sorted(failures):
        errors.append(f"FAIL: {package}::{test}")
    for package, test in sorted(skips):
        errors.append(f"SKIP is unvalidated: {package}::{test}")
    for package in sorted(package_failures):
        errors.append(f"package finished FAIL: {package}")
    for package in sorted(package_skips):
        errors.append(f"package SKIP is unvalidated: {package}")
    print(f"checker: tests={len(results)} pass={len(passes)} skip={len(skips)} fail={len(failures)}")
    if errors:
        for error in errors:
            print(f"checker: ERROR {error}", file=sys.stderr)
        return 1
    print("checker: all required Pi core/product roots and packages PASS; no FAIL or SKIP")
    return 0


if __name__ == "__main__":
    if sys.argv[1:] == ["--selector"]:
        names = [name for _, tests in REQUIRED_GROUPS.values() for name in tests]
        print("^(" + "|".join(names) + ")$")
        raise SystemExit(0)
    if len(sys.argv) != 2:
        print(f"usage: {sys.argv[0]} <go-test-jsonl> | --selector", file=sys.stderr)
        raise SystemExit(2)
    raise SystemExit(main(sys.argv[1]))
