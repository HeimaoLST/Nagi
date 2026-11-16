#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import sys
import tarfile
import urllib.error
import urllib.request


ROOT = Path(__file__).resolve().parent.parent


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_pi_inputs(pi: dict) -> None:
    for path_key, hash_key in (("package_json", "package_json_sha256"), ("lock_file", "lock_sha256")):
        path = (ROOT / pi[path_key]).resolve()
        if not path.is_relative_to(ROOT) or sha256(path) != pi[hash_key]:
            raise ValueError(f"Pi {path_key} differs from the pin")
    patch = pi["mcp_patch"]
    patch_path = (ROOT / patch["path"]).resolve()
    if patch["package"] != "@earendil-works/pi-mcp" or not patch_path.is_relative_to(ROOT) or sha256(patch_path) != patch["sha256"]:
        raise ValueError("Pi MCP patch differs from the pin")
    package = json.loads((ROOT / pi["package_json"]).read_text())
    lock = json.loads((ROOT / pi["lock_file"]).read_text())
    for name, expected in pi["packages"].items():
        locked = lock["packages"].get("node_modules/" + name, {})
        if package["dependencies"].get(name) != pi["version"] or any(locked.get(key) != value for key, value in expected.items()):
            raise ValueError(f"Pi package {name} differs from the pin")


def load_pin(path: Path) -> dict:
    pin = json.loads(path.read_text())
    node, pi = pin["node"], pin["pi"]
    if pin.get("schema") != 1 or (node["platform"], node["arch"]) != ("darwin", "arm64"):
        raise ValueError("pin must select schema1 macOS ARM64")
    version = node["version"]
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version) or tuple(map(int, version.split("."))) < (22, 19, 0):
        raise ValueError("Node pin must meet Pi's minimum22.19.0")
    expected_url = f"https://nodejs.org/dist/v{version}/node-v{version}-darwin-arm64.tar.gz"
    if node["archive_url"] != expected_url or not re.fullmatch(r"[0-9a-f]{64}", node["archive_sha256"]):
        raise ValueError("Node pin must identify one official archive and its SHA256")
    if pi["version"] != "1.5.2" or not re.fullmatch(r"[0-9a-f]{40}", pi["declared_source_sha"]):
        raise ValueError("Pi pin must identify1.5.2 and its declared source")
    if set(pi["packages"]) != {"@earendil-works/pi-coding-agent", "@earendil-works/pi-ai", "@earendil-works/pi-mcp"}:
        raise ValueError("Pi pin must bind its coding-agent, AI and MCP packages")
    verify_pi_inputs(pi)
    return pin


def fetch(pin_path: Path, output: Path) -> dict:
    pin_digest = sha256(pin_path)
    pin = load_pin(pin_path)
    output = output.resolve()
    if output.is_relative_to(ROOT):
        raise ValueError("output must be outside the source checkout")
    output.mkdir(parents=True, exist_ok=False)
    node = pin["node"]
    archive = output / Path(node["archive_url"]).name
    with urllib.request.urlopen(node["archive_url"], timeout=60) as response, archive.open("xb") as handle:
        shutil.copyfileobj(response, handle, length=1024 * 1024)
    if sha256(archive) != node["archive_sha256"]:
        raise ValueError("downloaded Node archive SHA256 differs from the pin; nothing extracted")
    prefix = f"node-v{node['version']}-darwin-arm64/"
    files = {}
    with tarfile.open(archive, "r:gz") as bundle:
        for relative in ("bin/node", "LICENSE"):
            member = bundle.getmember(prefix + relative)
            if not member.isfile():
                raise ValueError(f"Node archive lacks regular {relative}")
            destination = output / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            with bundle.extractfile(member) as source, destination.open("xb") as handle:
                shutil.copyfileobj(source, handle, length=1024 * 1024)
            destination.chmod(0o755 if relative == "bin/node" else 0o644)
            files[relative] = {"path": str(destination), "sha256": sha256(destination), "bytes": destination.stat().st_size}
    verify_pi_inputs(pin["pi"])
    if sha256(pin_path) != pin_digest:
        raise ValueError("runtime pin changed during fetch")
    receipt = {
        "schema": 1,
        "pin": {"path": str(pin_path), "sha256": pin_digest},
        "node": {**node, "archive_path": str(archive), "archive_sha256_verified": True},
        "pi": pin["pi"],
        "files": files,
        "verification": "Archive verified before extraction; Pi package, lock and repo-owned MCP patch bytes verified before and after fetch. Node was not executed; version/source/support declarations come from the pin. Pi packages were not installed or verified against their registry tarballs by this fetch.",
    }
    receipt_path = output / "manifest.json"
    receipt_path.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n")
    return {"node": files["bin/node"]["path"], "license": files["LICENSE"]["path"], "receipt": str(receipt_path), "receipt_sha256": sha256(receipt_path)}


def main() -> int:
    parser = argparse.ArgumentParser(description="Fetch the pinned official Node runtime and bind Pi input provenance without executing either.")
    parser.add_argument("--pin", type=Path, default=ROOT / ".github/pi-runtime.json")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = fetch(args.pin.resolve(), args.output)
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError, urllib.error.URLError) as error:
        print(f"Node runtime fetch failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
