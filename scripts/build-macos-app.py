#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import plistlib
import re
import shutil
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parent.parent
WORKER = ROOT / "internal/pi/worker"
PIN_PATH = ROOT / ".github/pi-runtime.json"
BUNDLE_ID = "dev.nagi.desktop"
MINIMUM_OS = "13.0"


def output(*argv: str | Path) -> str:
    return subprocess.check_output(argv, cwd=ROOT, text=True).strip()


def sha256(path: Path) -> str:
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def write_json(path: Path, value: dict) -> None:
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def inventory(root: Path) -> dict:
    files = {}
    for path in sorted(root.rglob("*")):
        name = path.relative_to(root).as_posix()
        if path.is_symlink():
            if not path.resolve(strict=True).is_relative_to(root):
                raise ValueError(f"{name} resolves outside {root}")
            files[name] = {"symlink": os.readlink(path)}
        elif path.is_file():
            files[name] = {"sha256": sha256(path), "bytes": path.stat().st_size}
    return files


def install_payload(payload: Path, destination: Path, pin: dict) -> dict:
    manifest = json.loads((payload / "manifest.json").read_text())
    if manifest["schema"] != 1 or manifest["pi"] != pin["pi"]:
        raise ValueError("worker manifest differs from the current Pi pin")
    if any(manifest["node"][key] != value for key, value in pin["node"].items()):
        raise ValueError("worker manifest differs from the current Node pin")
    inputs = [WORKER / name for name in ("package.json", "package-lock.json", "tsconfig.json")]
    inputs.extend(WORKER.glob("*.ts"))
    inputs.extend((PIN_PATH, ROOT / "assets/licenses/Pi-MIT.txt", ROOT / "scripts/build-pi-worker.py"))
    inputs.extend(ROOT / patch["path"] for patch in pin["pi"]["patches"])
    expected = {path.relative_to(ROOT).as_posix(): sha256(path) for path in inputs}
    if manifest["nagi_source"]["inputs"] != expected:
        raise ValueError("worker inputs differ from this checkout")
    if manifest["entry"] != {"node": "bin/node", "worker": "dist/worker.js"}:
        raise ValueError("worker entry points differ from the app's Resources/pi layout")

    destination.mkdir()
    for name in ("bin", "dist", "node_modules", "licenses"):
        shutil.copytree(payload / name, destination / name, symlinks=True)
    for name in ("package.json", "package-lock.json"):
        shutil.copy2(payload / name, destination / name)
    files = {name: entry for name, entry in manifest["files"].items() if name != "build.log"}
    if inventory(destination) != files:
        raise ValueError("installed worker bytes differ from the worker manifest")
    if files["bin/node"]["sha256"] != manifest["node"]["binary_sha256"]:
        raise ValueError("Node binary differs from its recorded runtime hash")
    if files["package-lock.json"]["sha256"] != pin["pi"]["lock_sha256"]:
        raise ValueError("bundled worker lock differs from the Pi pin")
    write_json(destination / "manifest.json", manifest)
    return manifest


def minimum_os(binary: Path) -> str:
    if output("/usr/bin/xcrun", "lipo", "-archs", binary) != "arm64":
        raise ValueError(f"{binary.name} must be Apple Silicon arm64")
    build = output("/usr/bin/xcrun", "vtool", "-show-build", binary)
    platform = re.search(r"^\s*platform\s+(\S+)\s*$", build, re.MULTILINE)
    minimum = re.search(r"^\s*minos\s+([0-9.]+)\s*$", build, re.MULTILINE)
    if platform is None or platform[1] != "MACOS" or minimum is None:
        raise ValueError(f"cannot read the macOS deployment target for {binary.name}")
    version = minimum[1]
    if tuple(map(int, (version + ".0.0").split(".")[:3])) > (13, 0, 0):
        raise ValueError(f"{binary.name} requires macOS {version}, above the {MINIMUM_OS} target")
    return version


def copy_notices(licenses: Path, node_license: Path, build_info: dict) -> dict:
    shutil.copytree(ROOT / "assets/licenses", licenses)
    shutil.copy2(node_license, licenses / "Node.txt")
    goroot = Path(output("go", "env", "GOROOT"))
    modules = build_info.get("Deps", [])
    for module in modules:
        identifier = module["Path"] + "@" + module["Version"]
        downloaded = json.loads(output("go", "mod", "download", "-json", identifier))
        directory = Path(downloaded["Dir"])
        notices = [path for path in directory.iterdir() if path.is_file() and path.stem.upper() in {"LICENSE", "NOTICE", "COPYING", "PATENTS", "COPYRIGHT"}]
        if not notices:
            raise ValueError(f"no license notice found for {identifier}")
        destination = licenses / "go" / identifier
        destination.mkdir(parents=True)
        for path in notices:
            shutil.copy2(path, destination / path.name)
    destination = licenses / "go" / build_info["GoVersion"]
    destination.mkdir(parents=True)
    for name in ("LICENSE", "PATENTS"):
        shutil.copy2(goroot / name, destination / name)
    return {"Contents/Resources/licenses/" + name: entry["sha256"] for name, entry in inventory(licenses).items()}


def build(destination: Path, payload: Path) -> dict:
    destination = destination.resolve()
    payload = payload.resolve(strict=True)
    if destination.is_relative_to(ROOT) or payload.is_relative_to(ROOT):
        raise ValueError("app output and worker payload must be outside the source checkout")
    source = {"commit": output("git", "rev-parse", "HEAD"), "status": output("git", "status", "--porcelain", "--untracked-files=all").splitlines()}
    if source["status"]:
        raise ValueError("package builds require a clean committed source checkout")
    pin = json.loads(PIN_PATH.read_text())
    version = (ROOT / "VERSION").read_text().strip()
    destination.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="assembly-", dir=destination) as staging:
        app = Path(staging) / "Nagi.app"
        contents = app / "Contents"
        resources = contents / "Resources"
        resources.mkdir(parents=True)
        worker_root = resources / "pi"
        runtime = install_payload(payload, worker_root, pin)
        shutil.copy2(PIN_PATH, resources / "pi-runtime.json")
        gui = contents / "MacOS/Nagi"
        gui.parent.mkdir()
        command = [sys.executable, "scripts/gui-go.py", "build", "-mod=readonly", "-trimpath", "-buildvcs=true", "-o", str(gui), "."]
        build_env = {"GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "0", "MACOSX_DEPLOYMENT_TARGET": MINIMUM_OS}
        with (destination / "build.log").open("w") as log:
            subprocess.run(command, cwd=ROOT, env={**os.environ, **build_env}, stdout=log, stderr=subprocess.STDOUT, check=True)
        build_info = json.loads(output("go", "version", "-m", "-json", gui))
        settings = {setting["Key"]: setting["Value"] for setting in build_info["Settings"]}
        if settings.get("vcs.revision") != source["commit"] or settings.get("vcs.modified") != "false":
            raise ValueError("GUI build information differs from the clean package source")
        node = worker_root / "bin/node"
        worker = worker_root / "dist/worker.js"
        floors = {"gui": minimum_os(gui), "node": minimum_os(node), "bundle": MINIMUM_OS}
        plist = {
            "CFBundleDevelopmentRegion": "en",
            "CFBundleDisplayName": "Nagi",
            "CFBundleExecutable": "Nagi",
            "CFBundleIdentifier": BUNDLE_ID,
            "CFBundleInfoDictionaryVersion": "6.0",
            "CFBundleName": "Nagi",
            "CFBundlePackageType": "APPL",
            "CFBundleShortVersionString": version,
            "CFBundleVersion": version,
            "LSMinimumSystemVersion": MINIMUM_OS,
            "NSHighResolutionCapable": True,
        }
        with (contents / "Info.plist").open("wb") as info:
            plistlib.dump(plist, info)
        licenses = copy_notices(resources / "licenses", worker_root / "licenses/Node.txt", build_info)
        if output("git", "rev-parse", "HEAD") != source["commit"] or output("git", "status", "--porcelain", "--untracked-files=all"):
            raise ValueError("source changed during packaging")
        manifest = {
            "schema": 1,
            "version": version,
            "nagi_source": source,
            "bundle": {"identifier": BUNDLE_ID, "executable": "Contents/MacOS/Nagi", "minimum_os": MINIMUM_OS, "arch": "arm64"},
            "minimum_os": floors,
            "gui": {"build_info": build_info, "unsigned_sha256": sha256(gui)},
            "node": runtime["node"],
            "pi": runtime["pi"],
            "runtime": {"manifest_sha256": sha256(worker_root / "manifest.json"), "worker_sha256": sha256(worker), "pin_sha256": sha256(PIN_PATH)},
            "licenses": licenses,
            "build": {"command": command, "cwd": str(ROOT), "environment": build_env},
        }
        write_json(resources / "manifest.json", manifest)
        subprocess.run(["/usr/bin/codesign", "--verify", "--strict", node], check=True)
        subprocess.run(["/usr/bin/codesign", "--force", "--sign", "-", "--timestamp=none", app], check=True)
        subprocess.run(["/usr/bin/codesign", "--verify", "--deep", "--strict", app], check=True)
        signature = subprocess.run(["/usr/bin/codesign", "--display", "--verbose=4", app], check=True, capture_output=True, text=True).stderr
        cdhash = re.search(r"^CDHash=([0-9a-f]+)$", signature, re.MULTILINE)
        if cdhash is None:
            raise ValueError("codesign returned no bundle CDHash")
        manifest["signature"] = {"kind": "ad-hoc", "cdhash": cdhash[1]}
        manifest["files"] = inventory(app)
        write_json(destination / "manifest.json", manifest)
        app.rename(destination / app.name)
    return {"app": str(destination / "Nagi.app"), "manifest": str(destination / "manifest.json"), "commit": source["commit"], "cdhash": manifest["signature"]["cdhash"]}


def main() -> int:
    parser = argparse.ArgumentParser(description="Assemble and ad hoc sign a standalone macOS Nagi application.")
    parser.add_argument("--payload", type=Path, required=True, help="External output of build-pi-worker.py")
    parser.add_argument("--output", type=Path, required=True, help="New external directory for Nagi.app and its receipts")
    args = parser.parse_args()
    try:
        print(json.dumps(build(args.output, args.payload), sort_keys=True))
    except (KeyError, OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"Nagi macOS app build failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
