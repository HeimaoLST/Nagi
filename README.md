# Nagi

Nagi is a desktop workbench for persistent Agent contacts, delegated issues, follow-up requirements and execution results. It is an independent Go application that consumes [Saki Runtime's version 1 stdio contract](https://github.com/HeimaoLST/Saki/blob/dev/docs/design/runtime-api.md). Saki owns model and tool execution, context management and session history; Nagi owns its product data and interface.

This repository preserves the Nagi product exported from Saki candidate `00298ae22bb95afc11366597e517a3b1ceba21e7`, including its existing regressions and tests. Repository separation does not complete the historical MVP, native acceptance, real-model A/B work or user trials. See [the documentation index](docs/README.md) and [import provenance](docs/design/source-import.json).

## Build and run

The supported product target is macOS arm64. Use Go 1.27.3, Python 3 and `patch`. Dependencies stay pinned in `go.mod` and `go.sum`. The launcher applies the existing MyGo v0.7.4 Split, editor and Markdown link patches in a temporary copy, checks their hashes, and leaves the module cache and repository module files unchanged.

```sh
python3 scripts/gui-go.py build -o /private/tmp/Nagi .
SAKI_RUNTIME_BIN=/absolute/path/to/Saki /private/tmp/Nagi
```

Provide a separately built compatible Saki executable. Nagi does not build Rust or search a sibling source checkout. An ordinary first launch lets the user configure the provider in Connection settings. The existing external environment configuration mode remains supported.

Runtime selection retains this order: `SAKI_RUNTIME_BIN`, helpers beside the GUI named `Saki-runtime` or `saki-runtime`, the existing Resources helper paths, then `Saki` on `PATH`. Nagi starts the selected executable with `--stdio`; it does not opt into metadata invalidations or enable automatic title jobs.

Existing data locations and IDs remain unchanged: `SAKI_GUI_STORE` or `<UserConfigDir>/Saki/contacts.json`, adjacent `connection.json`, and `SAKI_SESSION_DIR` or the adjacent `sessions/` directory. `SAKI_GUI_WORKDIR` remains supported. On macOS the default config root is normally `~/Library/Application Support/Saki`. This split does not move data or replay old tasks.

## Verify

Run from this repository root. Tests use headless UI fixtures; no native app or external model is required. Tests requiring an actual Saki binary explicitly skip when it is absent, so a plain Go PASS alone does not prove integration compatibility.

```sh
python3 scripts/gui-go.py test -race -count=1 ./...
python3 scripts/gui-go.py vet ./...
python3 -m unittest discover -s scripts -p 'test_macos_minimum.py'

# Exercise the existing real-process protocol and issue-tool tests with local providers.
python3 scripts/gui-go.py build -o /private/tmp/Nagi-issue-helper .
mkdir -p /private/tmp/nagi-previews
SAKI_TEST_RUNTIME_BIN=/absolute/path/to/Saki \
SAKI_TEST_ISSUE_TOOL_HELPER=/private/tmp/Nagi-issue-helper \
SAKI_PREVIEW_DIR=/private/tmp/nagi-previews SAKI_TEST_ONLINE=0 \
python3 scripts/gui-go.py test -race -json -count=1 ./... > /private/tmp/nagi-runtime-results.jsonl
python3 scripts/check-gui-runtime-results.py /private/tmp/nagi-runtime-results.jsonl
```

The helper uses the same executable's `--issue-tool-helper` entry point and does not create a GUI. Nagi's existing issue tools remain an application-owned MCP bridge; their transport and state are separate from Saki's Agent loop.

## Package macOS

Use a clean Nagi checkout, the Nagi release version in `VERSION`, and an explicitly provided macOS arm64 Saki binary whose `--version` reports its identity without starting a model. The runtime source SHA must come from that artifact's provenance.

```sh
scripts/build-macos-app.sh /absolute/output-directory \
  /absolute/path/to/prebuilt/Saki "$(cat VERSION)" FULL_RUNTIME_SOURCE_SHA
```

The script builds only Go, copies the supplied Runtime, selects the maximum of both Mach-O deployment minima, ad-hoc signs the copied helper and `Nagi.app`, then runs two existing offline protocol integration tests. It does not launch the native GUI, compile Saki/Cargo, or take Nagi's version from Saki. The bundle uses `Contents/MacOS/Nagi` and `Contents/MacOS/Saki-runtime`; the existing bundle identifier `dev.saki.desktop` is retained.

Outputs include `Nagi.app.zip`, `manifest.json` and `SHA256SUMS`. Manifest schema 2 records separate `nagi` and `runtime` identities. The top-level source fields refer to Nagi. Runtime fields distinguish the caller-supplied source SHA, self-reported version, original input hash and packaged hash after signing. The source SHA is provenance supplied by the caller, not an embedded-commit verification claim. The input Runtime is never re-signed in place.

`VERSION` is Nagi's version authority; the packaging argument must match it. Runtime compatibility is version 1 stdio with the required methods used by `runtime.go` and `connection_runtime.go`, not equality between the two products' source commits or release versions. Release CI owns artifact pinning and compatibility checks.
