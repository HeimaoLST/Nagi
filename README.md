# Nagi

Nagi is a native desktop workbench for persistent Bots, independent activities, follow-up requirements and execution results. Pi owns Agent execution, provider/tools and authoritative session transcripts; Nagi owns product identity, routing, state, receipts and the interface. The replacement is in progress; the current tree has not completed product cutover or standalone acceptance.

This repository preserves the Nagi product exported from Saki candidate `00298ae22bb95afc11366597e517a3b1ceba21e7`, including its existing regressions and tests. Repository separation does not complete the historical MVP, native acceptance, real-model A/B work or user trials. See [the documentation index](docs/README.md) and [import provenance](docs/design/source-import.json).

## Using Nagi

Nagi targets Apple Silicon on macOS 13 or later. The standalone Pi package is still in development. CI publishes the tested source, screenshots and worker build evidence. [Current CI](https://github.com/HeimaoLST/Nagi/actions/workflows/ci.yml)

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

The cleanup-first Pi integration is in progress under [issue31](https://github.com/HeimaoLST/Nagi/issues/31). The [integration contract](docs/design/pi-integration.md) defines the concrete client/worker, product ownership, current profile format and acceptance gates. This branch is an incomplete migration, not a usable Pi release. Existing consumer flows above remain the capabilities to preserve; provider/native acceptance has not been established for this changed tree.

Development uses `dev`; frozen integration/E2E candidates use `integration`; stable releases use `main`. The normal checkout is `~/Workspace/Nagi`, with the integration checkout at `~/Workspace/worktrees/Nagi/integration`. Additional worktrees are temporary task isolation, with an owner and cleanup condition. Do not modify a tested checkout during validation.

Use Go1.27.3, Python3 and `patch`. The MyGo launcher composes pinned patches in a temporary module copy without changing the module cache. The product target remains macOS13.0/ARM64. Pi1.5.2 requires Node>=22.19.0; the package owns a separately pinned runtime rather than relying on the consumer's PATH.

```sh
python3 scripts/gui-go.py build -o /private/tmp/Nagi .
python3 scripts/fetch-node-runtime.py --pin .github/pi-runtime.json --output /private/tmp/nagi-node
python3 scripts/build-pi-worker.py --runtime /private/tmp/nagi-node --output /private/tmp/nagi-pi-worker
```

A static build does not establish a working Pi-backed native app. Actual integration/E2E, synthetic-key accessibility, product tool/MCP, lifecycle and Node-independent package gates must pass on the combined tree. Developers do not add or modify unit tests; necessary unit maintenance belongs to the test owner.

Nagi starts with its own profile. On macOS the default directory is `~/Library/Application Support/Nagi`. It contains `contacts.json`, the private `connection.json` and Pi session data under `pi/`. Isolated developer fixtures can set `NAGI_GUI_STORE` and `NAGI_GUI_WORKDIR`. The application uses the current store format and current-format backups. Saki imports, session replay and legacy data recovery are retired.

The protected CI aggregate must use replacement Pi integration and package proofs before this migration is accepted. Missing replacement gates are not optional skips. Nagi version, Pi/dependency locks, Node archive/binary, signed package identity and notices are separate provenance. No Release or deployment follows from an intermediate build.
