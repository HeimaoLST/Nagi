# Nagi

Nagi is a desktop workbench for persistent Agent contacts, delegated issues, follow-up requirements and execution results. It is an independent Go application that consumes [Saki Runtime's version 1 stdio contract](https://github.com/HeimaoLST/Saki/blob/dev/docs/design/runtime-api.md). Saki owns model and tool execution, context management and session history; Nagi owns its product data and interface.

This repository preserves the Nagi product exported from Saki candidate `00298ae22bb95afc11366597e517a3b1ceba21e7`, including its existing regressions and tests. Repository separation does not complete the historical MVP, native acceptance, real-model A/B work or user trials. See [the documentation index](docs/README.md) and [import provenance](docs/design/source-import.json).

## Use the macOS preview

Development previews target Apple Silicon Macs and currently declare macOS 13.0 as their minimum. Native and independent-user acceptance is still in progress; follow the [complete-product acceptance record](https://github.com/HeimaoLST/Nagi/issues/7) for the tested scope of a particular build.

1. Open [Nagi CI](https://github.com/HeimaoLST/Nagi/actions/workflows/ci.yml) and select the maintainer-identified successful `dev` build. Under **Artifacts**, download `nagi-macos-arm64-<source SHA>`. GitHub requires sign-in and repository read access to download artifacts; see its [download instructions](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/download-workflow-artifacts).
2. Extract the downloaded artifact, then extract its `Nagi.app.zip`. Move `Nagi.app` to Applications and open it from Finder. Keep the accompanying `manifest.json` and `SHA256SUMS` so the build can be identified when reporting a problem.
3. CI previews use ad-hoc signing. For a developer-verification alert, consult [Apple's GUI instructions](https://support.apple.com/en-us/102445). A damaged-app or malware alert needs investigation; report the exact message.

Normal setup, file selection and the recovery actions below use the GUI. You can browse the workbench and prepare local contacts and drafts before connecting a model service.

### First connection

Obtain API access from your model service, including its API base URL, key, exact model ID and supported limits. Provider/model verification remains part of the build's acceptance scope; a compatible API shape alone does not establish model access or capacity.

1. Open **Settings → Connection & model**. Select the protocol supported by your service, then enter **API endpoint**, **API key** and **Default model**. Nagi adds the selected protocol path to the base URL.
2. Use **Choose folder…** to select the default working directory. Open **Capacity & execution limits** and check the context/output values against your service. Limits are not inferred from a model name; task slots describe local execution capacity.
3. Choose **Test connection and model** when you want an explicit model check. It uses the current form, may incur service charges, and leaves saved/applied settings unchanged.
4. While idle, choose **Save and apply**. Follow the displayed saved, applied or error state. If settings were saved but application failed, correct the reported condition and use **Apply saved**.

Saved fields appear as value rows; **Edit** opens their current draft. Closing an editor retains edits. **Save configuration** saves defaults without interrupting active work, while **Apply saved** applies the saved values when safe. Existing sessions keep their bound model, limits and working directory; inspect the displayed scope before starting new work. The API key is saved in a private local connection file without encryption.

### Contacts, files and results

Choose or create a contact and enter a request in its conversation. **General** settings contain appearance, theme and language choices; follow any restart note shown for system windows.

For a UTF-8 file, open **Task context → Add file → Choose UTF-8 file…**. Selection fills the path draft. **Save file** reads and saves the snapshot for preview; submission is a separate action. Later disk changes leave that snapshot intact until **Refresh from file**. Text can also be added with **Add snippet**. Review or remove materials before sending.

Use the corresponding issue/work/result and history views to inspect actual runs and outputs. A finished execution can leave its issue open. On failed or uncertain work, inspect the available details/history before explicitly continuing.

### Save failures and recovery

For a local save error, retain the open draft, correct the reported condition and use **Retry save**. Connection errors belong in **Connection & model**. Opening settings or details does not repeat a model request.

In **Settings → General → Workbench data**, **Save recovery copy…** exports saved contact/task records, saved drafts, requirements and attached text snapshots. It excludes unsaved edits, actual chat/tool history, API settings and keys, work files, theme files and language preferences; it is not a complete backup.

When startup cannot load workbench data, **Restore paused…** lets you select a compatible copy, review its preview and explicitly confirm **Restore paused**. An existing original is archived before replacement. Read the outcome before taking another action:

| Outcome | Next step |
| --- | --- |
| Verified installation | The current window still cannot save or run work. Quit and reopen to load the verified restored records, with automatic continuation paused and pending/unknown records retained. |
| Not installed | The original was not replaced. Keep any listed archive and review Details before a new explicit attempt. |
| Installation unconfirmed | Nagi has not confirmed the on-disk result; the current window cannot save or run work. Keep the listed locations and Details. Check the result before another attempt; do not automatically repeat or roll back. |

If the current window cannot save, copy any unsaved text you need elsewhere before quitting. A loaded workbench or failed local save uses its existing save/retry path instead of replacement. Missing Runtime history is reported separately; restoring workbench records does not recreate it or resend work.

## Build and run

For contributors: the supported product target is macOS arm64. Use Go 1.27.3, Python 3 and `patch`. Dependencies stay pinned in `go.mod` and `go.sum`. The launcher applies the existing MyGo v0.7.4 Split, editor, Markdown link and presentation patches in a temporary copy, checks their hashes, and leaves the module cache and repository module files unchanged.

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
python3 scripts/gui-go.py test -race -timeout=20m -count=1 ./...
python3 scripts/gui-go.py vet ./...
python3 -m unittest discover -s scripts -p 'test_macos_minimum.py'

# Exercise the existing real-process protocol and issue-tool tests with local providers.
python3 scripts/gui-go.py build -o /private/tmp/Nagi-issue-helper .
mkdir -p /private/tmp/nagi-previews
SAKI_TEST_RUNTIME_BIN=/absolute/path/to/Saki \
SAKI_TEST_ISSUE_TOOL_HELPER=/private/tmp/Nagi-issue-helper \
SAKI_PREVIEW_DIR=/private/tmp/nagi-previews SAKI_TEST_ONLINE=0 \
python3 scripts/gui-go.py test -race -timeout=20m -json -count=1 ./... > /private/tmp/nagi-runtime-results.jsonl
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
