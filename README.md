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
2. Use **Choose folder…** to select the default working directory. Working context/output and slot defaults are provided. **Capacity & execution limits** contains advanced overrides; declared limits are not inferred from a model name or verified provider capacity.
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

## Development during the Pi migration

The cleanup-first Pi integration is in progress under [issue31](https://github.com/HeimaoLST/Nagi/issues/31). The [integration contract](docs/design/pi-integration.md) defines the concrete client/worker, product ownership, legacy-data fence and acceptance gates. This branch is an incomplete migration, not a usable Pi release. Existing consumer flows above remain the capabilities to preserve; provider/native acceptance has not been established for this changed tree.

Use Go1.27.3, Python3 and `patch`. The MyGo launcher composes pinned patches in a temporary module copy without changing the module cache. The product target remains macOS13.0/ARM64. Pi1.5.2 requires Node>=22.19.0; the package owns a separately pinned runtime rather than relying on the consumer's PATH.

```sh
python3 scripts/gui-go.py build -o /private/tmp/Nagi .
cd internal/pi/worker
npm ci --ignore-scripts --no-audit --no-fund
npm run build
```

A static build does not establish a working Pi-backed native app. Actual integration/E2E, synthetic-key accessibility, product tool/MCP, lifecycle and Node-independent package gates must pass on the combined tree. Developers do not add or modify unit tests; necessary unit maintenance belongs to the test owner.

Existing data locations and IDs remain unchanged: `SAKI_GUI_STORE` or `<UserConfigDir>/Saki/contacts.json`, adjacent `connection.json`, and `SAKI_SESSION_DIR` or the adjacent `sessions/` directory. `SAKI_GUI_WORKDIR` remains supported. On macOS the default config root is normally `~/Library/Application Support/Saki`. This split does not move data or replay old tasks.

Legacy conversation files and product references remain preserved. The archive-versus-readable product choice is pending; the migration does not reinterpret old references or replay work. The previous Saki launcher/artifact/package instructions are [historical at57e0a678](https://github.com/HeimaoLST/Nagi/blob/57e0a6784ba7f8ec4c37e5a76f6ab8cdccd9542c/README.md#build-and-run), not the target production architecture or instructions to resume Saki development.

The protected CI aggregate must use replacement Pi integration and package proofs before this migration is accepted. Missing replacement gates are not optional skips. Nagi version, Pi/dependency locks, Node archive/binary, signed package identity and notices are separate provenance. No Release or deployment follows from an intermediate build.
