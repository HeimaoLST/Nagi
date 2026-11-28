#!/usr/bin/env python3
"""Run Go through the temporary pinned MyGo patches, including secure accessibility.

Usage: python3 scripts/gui-go.py test|vet|build|run [Go arguments].
The real go.mod, module cache, and source tree are not rewritten.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
MYGO = "github.com/egoist/mygo"
VERSION = "v0.10.0"
MARKDOWN_VIEW = "ui/markdown/view.go"
LINK_VIEW = "d4f2be61d304e0bbf6b77a324a53bdfb6ea1119140bb963067dc3531a3d01adf"
PATCHES = (
    ("split-release.patch", {
        "ui/split_release_before_frame_test.go": None,
    }, {
        "ui/split_release_before_frame_test.go": "d1604500d8043d39db15e6e2d11fd14f31c6fb4c543db1e334d86d670574b124",
    }),
    ("markdown-link-focus.patch", {
        MARKDOWN_VIEW: "d1a756855d1a6fafbf51c4417d408f35de821af1ff2eac2bb21f229173a40b4b",
    }, {
        MARKDOWN_VIEW: LINK_VIEW,
    }),
    ("markdown-presentation.patch", {
        "ui/markdown/theme.go": "edb4b59a2a01951e3cfdbcdc812596552eaade8d3a7a1485a13f1500fe58f121",
        MARKDOWN_VIEW: LINK_VIEW,
        "ui/markdown/spans.go": "272974925394a9c0d332e2176856ba48b0d23723c34156e1c4da90d9dcd0a6a7",
        "ui/markdown/copy.go": "00c3c39cd62464bf47212937293cf01f8ecad79d17bf338028896c0894461947",
        "ui/markdown/view_test.go": "d2221845614c64b72ec49597930c6e9aaecf06581cce5a084e9eafbc4274adff",
    }, {
        "ui/markdown/theme.go": "047578f076d84905420fac18c53d9fb023fd93ad9b26c0dc942f117d7fb184f8",
        MARKDOWN_VIEW: "fd1379d61282ce9af63cb3f5c2457a1413a86eafe7440b397933a990ab714d0a",
        "ui/markdown/spans.go": "e52916ed9e73bc36b6e8dc957d25c678a4aeb4669adc4b2637e2633298e92474",
        "ui/markdown/copy.go": "9e3096458fba3e4c6d559520175d523760fd8a5e0d226006def5c52eff70075f",
        "ui/markdown/view_test.go": "d5eb2fe33f3458cb1a0b598137e0c3e252fdcf31cdef8f729969c01e180211f8",
    }),
    ("secure-accessibility.patch", {
        "ui/access.go": "79c8350f3b29470d509f6be5f339bf91a5f356b98635dc7256c2852646eed8c5",
        "internal/darwin/access.go": "d3bfa7ad9b77736f18483e969d179eb91257a0077f2703a3cb633ca20976bbed",
    }, {
        "ui/access.go": "af0936ef292e9ee4dc0ad167bf33f0a0e5a7c772a7e81aa66ffd6f6e65eeee3b",
        "internal/darwin/access.go": "52ddeb89928ee00df3ec07432f30f77a2cc7819e2580d3bff1fc9da58b96f97e",
    }),
)


def fail(message: str, code: int) -> int:
    print("gui-go: " + message, file=sys.stderr)
    return code


def sha256(path: Path) -> str:
    import hashlib

    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def selected_module() -> dict:
    """Return Go's selected module metadata, including any Replace."""
    raw = subprocess.check_output(["go", "list", "-m", "-json", MYGO], cwd=ROOT, text=True)
    return json.loads(raw)


def module_dir() -> Path:
    raw = subprocess.check_output(
        ["go", "mod", "download", "-json", MYGO + "@" + VERSION],
        cwd=ROOT,
        text=True,
    )
    return Path(json.loads(raw)["Dir"])


def make_writable(path: Path) -> None:
    for root, dirs, files in os.walk(path):
        os.chmod(root, 0o755)
        for name in dirs:
            os.chmod(Path(root) / name, 0o755)
        for name in files:
            os.chmod(Path(root) / name, 0o644)


def main(argv: list[str]) -> int:
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__, file=sys.stderr)
        return 0 if argv else 2
    if argv[0] not in ("test", "vet", "build", "run"):
        return fail("this launcher only forwards test, vet, build and run", 2)
    if any(arg == "-modfile" or arg.startswith("-modfile=") for arg in argv[1:]):
        return fail("caller may not override the prepared -modfile", 2)
    if any(not (ROOT / "patches" / name).is_file() for name, _, _ in PATCHES):
        return fail("root module or a pinned patch is missing", 2)
    selected = selected_module()
    if selected.get("Version") != VERSION or selected.get("Replace") is not None:
        return fail(
            "selected MyGo is %s with replace=%r; update or remove the pinned patches"
            % (selected.get("Version"), selected.get("Replace")),
            2,
        )

    src = module_dir()
    with tempfile.TemporaryDirectory(prefix="xuhu-gui-go-") as td:
        tmp = Path(td)
        module = tmp / "mygo"
        shutil.copytree(src, module)
        make_writable(module)

        for name, before, after in PATCHES:
            for path, expected in before.items():
                source = module / path
                actual = sha256(source) if source.exists() else None
                if actual != expected:
                    return fail(f"{name}: unexpected {path} input sha256={actual}, expected {expected}", 2)
            with (ROOT / "patches" / name).open("rb") as patch:
                patch_result = subprocess.run(
                    ["patch", "-p1", "--fuzz=0", "--batch"],
                    cwd=module,
                    stdin=patch,
                    stdout=sys.stderr,
                    stderr=sys.stderr,
                )
            if patch_result.returncode != 0:
                return fail(f"{name} failed; refusing to run partial source", 3)
            for path, expected in after.items():
                actual = sha256(module / path)
                if actual != expected:
                    return fail(f"{name}: unexpected {path} output sha256={actual}, expected {expected}", 4)
            print(f"gui-go: verified {name} " + json.dumps(after, sort_keys=True), file=sys.stderr)

        # A module file distinct from the real go.mod; Go uses the
        # matching app.sum next to it. The source checkout is never edited.
        modfile = tmp / "app.mod"
        sumfile = tmp / "app.sum"
        shutil.copyfile(ROOT / "go.mod", modfile)
        shutil.copyfile(ROOT / "go.sum", sumfile)
        edit = subprocess.run(
            ["go", "mod", "edit", "-modfile=" + str(modfile), "-replace=" + MYGO + "=" + str(module)],
            cwd=ROOT,
            stdout=sys.stderr,
            stderr=sys.stderr,
        )
        if edit.returncode != 0:
            return fail("could not add the prepared module replace", 5)

        cmd = ["go", argv[0], "-modfile=" + str(modfile), *argv[1:]]
        rc = subprocess.run(cmd, cwd=ROOT).returncode
        return 128 + (-rc) if rc < 0 else rc


if __name__ == "__main__":
    try:
        sys.exit(main(sys.argv[1:]))
    except subprocess.CalledProcessError as exc:
        print("gui-go: preparation command failed: %s" % exc, file=sys.stderr)
        sys.exit(exc.returncode or 1)
