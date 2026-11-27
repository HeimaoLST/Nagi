#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import shutil
import subprocess
import sys


ROOT = Path(__file__).resolve().parent.parent
SOURCE = ROOT / "internal" / "pi" / "worker"
CATALOG_INPUTS = ("model-ranks.json", "model-catalog-supplement.json")
NOTICE_FILES = ("Pi-MIT.txt", "models.dev-MIT.txt")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_patch_files(package_root: Path, patch: dict, field: str) -> None:
    for name, hashes in patch["files"].items():
        if sha256(package_root / name) != hashes[field]:
            raise ValueError(f"{patch['package']}:{name} differs from {field}")


def source_inputs() -> dict:
    paths = [SOURCE / name for name in ("package.json", "package-lock.json", "tsconfig.json", "pty-supervisor.c")]
    paths.extend(SOURCE.glob("*.ts"))
    paths.extend((ROOT / ".github" / "pi-runtime.json", ROOT / "assets/licenses/Pi-MIT.txt", Path(__file__).resolve()))
    pin = json.loads((ROOT / ".github" / "pi-runtime.json").read_text())
    paths.extend(ROOT / patch["path"] for patch in pin["pi"]["patches"])
    return {str(path.relative_to(ROOT)): sha256(path) for path in paths}


def catalog_inputs() -> dict:
    paths = [*(SOURCE / name for name in CATALOG_INPUTS), ROOT / "assets/licenses/models.dev-MIT.txt", ROOT / "scripts/sync-model-catalog.py"]
    return {str(path.relative_to(ROOT)): sha256(path) for path in paths}


def build(output: Path, runtime: Path) -> dict:
    output = output.resolve()
    runtime = runtime.resolve()
    if output.is_relative_to(ROOT):
        raise ValueError("worker output must be outside the source checkout")
    before = source_inputs()
    catalog_before = catalog_inputs()
    pin = json.loads((ROOT / ".github" / "pi-runtime.json").read_text())
    for field, hash_field in (("package_json", "package_json_sha256"), ("lock_file", "lock_sha256")):
        if before[pin["pi"][field]] != pin["pi"][hash_field]:
            raise ValueError("worker package inputs differ from the runtime pin")
    receipt_path = runtime / "manifest.json"
    receipt = json.loads(receipt_path.read_text())
    if receipt["node"]["archive_sha256"] != pin["node"]["archive_sha256"] or not receipt["node"]["archive_sha256_verified"]:
        raise ValueError("Node receipt does not match the pinned official archive")
    for name in ("bin/node", "LICENSE"):
        if sha256(runtime / name) != receipt["files"][name]["sha256"]:
            raise ValueError("Node input bytes differ from their verified receipt")
    npm = shutil.which("npm")
    if npm is None:
        raise ValueError("npm is required to build the worker payload, not to use the packaged app")
    output.mkdir(parents=True, exist_ok=False)
    (output / "bin").mkdir()
    (output / "licenses").mkdir()
    shutil.copy2(runtime / "bin" / "node", output / "bin" / "node")
    shutil.copy2(runtime / "LICENSE", output / "licenses" / "Node.txt")
    for name in NOTICE_FILES:
        shutil.copy2(ROOT / "assets" / "licenses" / name, output / "licenses" / name)
    for name in ("package.json", "package-lock.json"):
        shutil.copy2(SOURCE / name, output / name)
    build_source = output / ".build-source"
    build_source.mkdir()
    for path in [SOURCE / "tsconfig.json", SOURCE / "pty-supervisor.c", *SOURCE.glob("*.ts")]:
        shutil.copy2(path, build_source / path.name)
    compiled_inputs = {path.name: sha256(path) for path in build_source.iterdir()}
    node = output / "bin" / "node"
    patches = pin["pi"]["patches"]
    package_roots = [output / "node_modules" / patch["package"] for patch in patches]
    for patch in patches:
        if sha256(ROOT / patch["path"]) != patch["sha256"]:
            raise ValueError("Pi SDK patch differs from the pin")
    compiler_root = output / "node_modules" / "typescript"
    compiler = compiler_root / "bin" / "tsc"
    environment = os.environ.copy()
    environment["PATH"] = str(node.parent) + os.pathsep + environment.get("PATH", "")
    dependency_command = [npm, "ci", "--ignore-scripts", "--no-audit", "--no-fund"]
    patch_commands = [["/usr/bin/patch", "--batch", "--forward", "--fuzz=0", "-p1", "-i", str(ROOT / patch["path"])] for patch in patches]
    compile_command = [str(node), str(compiler), "--project", str(build_source / "tsconfig.json"), "--noEmit", "false", "--outDir", str(output / "dist")]
    supervisor = output / "bin" / "nagi-pty-supervisor"
    native_flags = ["-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "-arch", "arm64",
                    "-mmacosx-version-min=13.0", "-fno-ident"]
    native_compile = ["/usr/bin/xcrun", "clang", *native_flags, "pty-supervisor.c", "-o", str(supervisor)]
    native_sign = ["/usr/bin/codesign", "--force", "--sign", "-", "--identifier", "dev.nagi.pty-supervisor", "--timestamp=none", str(supervisor)]
    native_verify = ["/usr/bin/codesign", "--verify", "--strict", str(supervisor)]
    native_compiler = subprocess.check_output(["/usr/bin/xcrun", "clang", "--version"], text=True).strip()
    catalog_command = [str(node), str(output / "dist" / "model-catalog-build.js")]
    prune_command = [npm, "prune", "--omit=dev", "--ignore-scripts", "--no-audit", "--no-fund"]
    commands = [dependency_command, *patch_commands, compile_command, catalog_command, prune_command, native_compile, native_sign, native_verify]
    with (output / "build.log").open("wb") as log:
        subprocess.run(dependency_command, cwd=output, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
        for patch, command, package_root in zip(patches, patch_commands, package_roots):
            verify_patch_files(package_root, patch, "before_sha256")
            subprocess.run(command, cwd=package_root, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
            verify_patch_files(package_root, patch, "after_sha256")
        lock = json.loads((output / "package-lock.json").read_text())
        compiler_package = json.loads((compiler_root / "package.json").read_text())
        if compiler_package["version"] != lock["packages"]["node_modules/typescript"]["version"]:
            raise ValueError("installed TypeScript compiler differs from the lock")
        compilation = {
            "source": compiled_inputs,
            "compiler": {
                "version": compiler_package["version"],
                "lock": lock["packages"]["node_modules/typescript"],
                "files": {str(path.relative_to(compiler_root)): sha256(path) for path in sorted(compiler_root.rglob("*")) if path.is_file()},
            },
            "development_install": json.loads((output / "node_modules" / ".package-lock.json").read_text()),
        }
        subprocess.run(compile_command, cwd=output, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
        for name in CATALOG_INPUTS:
            shutil.copy2(SOURCE / name, output / "dist" / name)
        subprocess.run(catalog_command, cwd=output, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
        (output / "dist" / "model-catalog-build.js").unlink()
        (output / "dist" / "model-catalog-supplement.json").unlink()
        subprocess.run(prune_command, cwd=output, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
        for native_command in (native_compile, native_sign, native_verify):
            subprocess.run(native_command, cwd=build_source, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
    native_build = subprocess.check_output(["/usr/bin/xcrun", "vtool", "-show-build", str(supervisor)], text=True)
    native_sdk = re.search(r"^\s*sdk\s+(\S+)\s*$", native_build, re.MULTILINE)
    if native_sdk is None:
        raise ValueError("cannot read the linked SDK version for the native supervisor")
    if {path.name: sha256(path) for path in build_source.iterdir()} != compiled_inputs:
        raise ValueError("worker compilation source changed during build")
    for path in build_source.iterdir():
        path.unlink()
    build_source.rmdir()
    for name in ("package.json", "package-lock.json"):
        if sha256(output / name) != before[str((SOURCE / name).relative_to(ROOT))]:
            raise ValueError("worker dependency operation changed the locked package inputs")
    for name, expected in pin["pi"]["packages"].items():
        actual = json.loads((output / "node_modules" / name / "package.json").read_text())
        if actual["version"] != expected["version"]:
            raise ValueError("installed Pi package differs from the pinned version")
    for patch, package_root in zip(patches, package_roots):
        verify_patch_files(package_root, patch, "after_sha256")
    if source_inputs() != before or catalog_inputs() != catalog_before:
        raise ValueError("worker source inputs changed during build")
    files = {}
    for path in sorted(output.rglob("*")):
        if path == output / "build.log":
            continue
        if path.is_symlink():
            files[str(path.relative_to(output))] = {"symlink": os.readlink(path)}
        elif path.is_file():
            files[str(path.relative_to(output))] = {"sha256": sha256(path), "bytes": path.stat().st_size}
    git_head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    git_status = subprocess.check_output(["git", "status", "--porcelain"], cwd=ROOT, text=True).splitlines()
    manifest = {
        "schema": 1,
        "nagi_source": {"commit": git_head, "status": git_status, "inputs": before},
        "pi": pin["pi"],
        "node": {**pin["node"], "input_receipt_sha256": sha256(receipt_path), "binary_sha256": sha256(node)},
        "commands": [[part.replace(str(output) + os.sep, "${OUTPUT}/").replace(str(ROOT) + os.sep, "${SOURCE}/")
                      for part in command] for command in commands],
        "command_cwds": [".", *(str(path.relative_to(output)) for path in package_roots), ".", ".", ".", *([build_source.name] * 3)],
        "catalog": {"path": "dist/model-catalog.json", "sha256": files["dist/model-catalog.json"]["sha256"], "inputs": catalog_before},
        "compilation": compilation,
        "supervisor": {"compiler": native_compiler, "sdk": native_sdk[1], "path": "bin/nagi-pty-supervisor",
                       "arch": "arm64", "minimum_os": "13.0",
                       "binary_sha256": files["bin/nagi-pty-supervisor"]["sha256"]},
        "entry": {"node": "bin/node", "worker": "dist/worker.js"},
        "files": files,
        "limits": "Ad-hoc signed native helper; native app, real-provider and product-data acceptance are separate. The reused Node receipt records its original Pi inputs; this manifest separately binds the current worker inputs."
    }
    destination = output / "manifest.json"
    destination.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")
    return {"output": str(output), "node": str(node), "worker": str(output / "dist" / "worker.js"), "manifest": str(destination), "manifest_sha256": sha256(destination)}


def main() -> int:
    parser = argparse.ArgumentParser(description="Build the pinned Pi worker payload outside the source checkout.")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--runtime", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = build(args.output, args.runtime)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as error:
        print(f"Pi worker build failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
