#!/usr/bin/env python3
"""Run Go through the temporary pinned editor, Split and Markdown link patches.

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
SPLIT_PATCH = ROOT / "patches" / "split-release.patch"
EDITOR_PATCH = ROOT / "patches" / "editor-release.patch"
MARKDOWN_LINK_PATCH = ROOT / "patches" / "markdown-link-focus.patch"
MYGO = "github.com/egoist/mygo"
VERSION = "v0.7.4"
SPLIT_BEFORE = "70f6d9b7e546278a54179d554b3935f2b8d3afc1e0df37e56fe0ad9f694c81a8"
SPLIT_AFTER = "315165f4974f652521d52696cb22207819d4ec7bd493d878da9ec6fcdf513fcf"
EDITOR_INPUT_BEFORE = "d7a8635820d7f5583f82d1b4f5b2c17ed6ebdbcae154daa4afafcdcb36395137"
EDITOR_INPUT_AFTER = "e6d64ecaa49201256a8bcd64bf22954c0bef5ec49961b6231a47b998dedbc8d7"
MARKDOWN_VIEW_BEFORE = "194e6719641a9dee4132e30d69a949e47ff9cd087fbdecf3f6ee84bb66b09b90"
MARKDOWN_VIEW_AFTER = "b80afdb2a433dd73722d97e372661e3a27f59f10588fc9efca01241e5379efc2"


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
    if not ROOT.is_dir() or not SPLIT_PATCH.is_file() or not EDITOR_PATCH.is_file() or not MARKDOWN_LINK_PATCH.is_file():
        return fail("root module or a pinned patch is missing", 2)
    selected = selected_module()
    if selected.get("Version") != VERSION or selected.get("Replace") is not None:
        return fail(
            "selected MyGo is %s with replace=%r; update or remove the pinned patches"
            % (selected.get("Version"), selected.get("Replace")),
            2,
        )

    src = module_dir()
    with tempfile.TemporaryDirectory(prefix="nagi-gui-go-") as td:
        tmp = Path(td)
        module = tmp / "mygo"
        shutil.copytree(src, module)
        make_writable(module)

        split_before = sha256(module / "ui" / "split.go")
        if split_before != SPLIT_BEFORE:
            return fail(
                "unexpected MyGo Split source sha256=%s, expected %s; refusing to patch"
                % (split_before, SPLIT_BEFORE),
                2,
            )
        editor_before = sha256(module / "ui" / "input.go")
        if editor_before != EDITOR_INPUT_BEFORE:
            return fail(
                "unexpected MyGo editor source sha256=%s, expected %s; refusing to patch"
                % (editor_before, EDITOR_INPUT_BEFORE),
                2,
            )
        markdown_before = sha256(module / "ui" / "markdown" / "view.go")
        if markdown_before != MARKDOWN_VIEW_BEFORE:
            return fail(
                "unexpected MyGo Markdown source sha256=%s, expected %s; refusing to patch"
                % (markdown_before, MARKDOWN_VIEW_BEFORE),
                2,
            )
        for label, patch in (("Split", SPLIT_PATCH), ("Editor", EDITOR_PATCH), ("Markdown link", MARKDOWN_LINK_PATCH)):
            patch_result = subprocess.run(
                ["patch", "-p1", "--fuzz=0", "--batch"],
                cwd=module,
                stdin=patch.open("rb"),
                stdout=sys.stderr,
                stderr=sys.stderr,
            )
            if patch_result.returncode != 0:
                return fail("%s patch failed; refusing to run partial source" % label, 3)
        split_after = sha256(module / "ui" / "split.go")
        if split_after != SPLIT_AFTER:
            return fail(
                "unexpected patched Split source sha256=%s, expected %s"
                % (split_after, SPLIT_AFTER),
                4,
            )
        editor_after = sha256(module / "ui" / "input.go")
        if editor_after != EDITOR_INPUT_AFTER:
            return fail(
                "unexpected patched editor source sha256=%s, expected %s"
                % (editor_after, EDITOR_INPUT_AFTER),
                4,
            )
        markdown_after = sha256(module / "ui" / "markdown" / "view.go")
        if markdown_after != MARKDOWN_VIEW_AFTER:
            return fail(
                "unexpected patched Markdown source sha256=%s, expected %s"
                % (markdown_after, MARKDOWN_VIEW_AFTER),
                4,
            )
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
