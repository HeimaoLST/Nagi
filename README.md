# Nagi

Nagi is an open-source native desktop app for working with long-lived Bots. Configure a connection, give a Bot several tasks in conversation, follow up, stop a specific execution and retrieve its results. After reopening the app, check the original work and any uncertain delivery. See the [v1 user flows](docs/requirements/desktop.md#v1).

The current integration uses Pi for execution, tools and transcripts. Nagi owns Bot identity, routing, state, receipts and the MyGo interface. The [documentation index](docs/README.md) links the product contract, native UI and review criteria.

## Using Nagi

Nagi targets Apple Silicon on macOS 13 or later. The standalone app bundles its own Node and Pi worker. CI builds the app and verifies startup with an isolated profile. [Current CI](https://github.com/HeimaoLST/Nagi/actions/workflows/ci.yml)

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

Ask the Bot in its conversation to check a task, follow up or retrieve a result. One Bot can handle several tasks. Stopping a chat targets that conversation execution. A finished execution can leave its task open. If delivery is uncertain, use the available details or history to check the original work before deciding whether to continue.

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

## Development

Use Go1.27.3, Python3 and `patch`. The MyGo launcher composes pinned patches in a temporary module copy without changing the module cache. The product target remains macOS13.0/ARM64. Pi1.5.2 requires Node>=22.19.0; the package owns a separately pinned runtime rather than relying on the consumer's PATH.

```sh
python3 scripts/gui-go.py build -o /private/tmp/Nagi .
python3 scripts/fetch-node-runtime.py --pin .github/pi-runtime.json --output /private/tmp/nagi-node
python3 scripts/build-pi-worker.py --runtime /private/tmp/nagi-node --output /private/tmp/nagi-pi-worker
python3 scripts/build-macos-app.py --payload /private/tmp/nagi-pi-worker --output /private/tmp/nagi-app
```

Run the assembler from a clean checkout with a fresh output directory. It writes an ad hoc signed `Nagi.app` and an adjacent `manifest.json`. For standalone acceptance, set `NAGI_TEST_APP_BUNDLE` to the absolute path of that app and keep its manifest alongside it.

The [Pi integration](docs/design/pi-integration.md) describes the runtime and [desktop v1](docs/requirements/desktop.md#v1) defines user-visible behavior. On macOS the default profile directory is `~/Library/Application Support/Nagi`. It holds the product profile, UI selection and preferences, private connection configuration and Pi session data. Use an isolated profile for development and acceptance.
