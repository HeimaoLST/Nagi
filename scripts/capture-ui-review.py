#!/usr/bin/env python3
"""Capture headless UI review evidence from a frozen Nagi checkout."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import struct
import subprocess
import sys


def git(root, *args):
    return subprocess.check_output(["git", *args], cwd=root, text=True).strip()


def capture(root, output, tests):
    candidate = git(root, "rev-parse", "HEAD")
    if git(root, "status", "--porcelain", "--untracked-files=all"):
        raise ValueError("capture requires a clean, frozen checkout")
    if output.is_relative_to(root) or root.is_relative_to(output):
        raise ValueError("choose a new output directory outside the checkout")
    if os.path.lexists(root / "connection.json"):
        raise ValueError("capture requires an isolated checkout without connection.json")

    output.mkdir(parents=True, exist_ok=False)
    shots = output / "shots"
    shots.mkdir()
    selector = "^(" + "|".join(tests) + ")$"
    command = [sys.executable, "scripts/gui-go.py", "test", "-mod=readonly", "-count=1",
               "-parallel=1", "-json", "-run", selector, "."]
    env = dict(os.environ, CGO_ENABLED="0", NAGI_TEST_ONLINE="0", NAGI_PREVIEW_DIR=str(shots))
    log = output / "test.jsonl"
    with log.open("wb") as stdout, (output / "test.stderr").open("wb") as stderr:
        result = subprocess.run(command, cwd=root, env=env, stdout=stdout, stderr=stderr)

    problems = []
    roots = dict.fromkeys(tests, "missing")
    package = "missing"
    try:
        for line in log.read_bytes().splitlines():
            event = json.loads(line)
            action = event.get("Action")
            if action in {"pass", "fail", "skip"}:
                test = event.get("Test")
                if test in roots:
                    roots[test] = action
                elif test is None:
                    package = action
    except ValueError:
        problems.append("invalid Go test JSON log")
    if result.returncode or package != "pass" or any(value != "pass" for value in roots.values()):
        problems.append("preview command or selected tests did not complete successfully")
    if git(root, "rev-parse", "HEAD") != candidate or git(root, "status", "--porcelain", "--untracked-files=all"):
        problems.append("checkout changed during capture")

    images = []
    for path in sorted(shots.glob("*.png")):
        data = path.read_bytes()
        if len(data) < 24 or data[:16] != b"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR":
            problems.append(f"invalid PNG header: {path.name}")
            continue
        width, height = struct.unpack(">II", data[16:24])
        images.append({"path": str(path.relative_to(output)), "width_px": width,
                       "height_px": height, "sha256": hashlib.sha256(data).hexdigest()})
    if not images:
        problems.append("capture did not produce PNG originals")

    record = {"commit": candidate, "evidence_grade": "H", "command": command,
              "command_exit": result.returncode, "tests": roots, "package": package,
              "images": images, "problems": problems}
    (output / "capture.json").write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    print(f"Headless capture: {len(images)} PNG originals at {output}")
    for problem in problems:
        print(problem, file=sys.stderr)
    return 1 if problems else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output", type=Path, help="new directory outside the checkout")
    parser.add_argument("--test", action="append", help="existing TestPreview root, repeatable")
    args = parser.parse_args()
    tests = list(dict.fromkeys(args.test or ["TestPreviewImages"]))
    if any(not re.fullmatch(r"TestPreview[A-Za-z0-9_]+", test) for test in tests):
        parser.error("--test requires an exact TestPreview root name")
    try:
        root = Path(git(Path.cwd(), "rev-parse", "--show-toplevel")).resolve()
        return capture(root, args.output.resolve(), tests)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"UI capture: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
