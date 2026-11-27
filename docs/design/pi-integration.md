# Pi execution in the desktop app

The native app starts an owned Node process running the bundled Pi worker. The Go client opens the product profile and routes UI actions and events. The TypeScript worker saves Bot identities, conversations, activities, questions, captured configuration and operation receipts. Pi owns model execution, tool dispatch and the authoritative session transcripts.

## Startup and shutdown

The installed app resolves Node, the worker, and the bundled Darwin helper (`bin/nagi-pty-supervisor`) under `Contents/Resources/pi` without `PATH`. It supplies the Pi state directory, product profile and private connection file at launch. The UI reads the saved snapshot and the selected conversation before displaying restored state. On reconnect it reads the original operation and execution identifiers. The worker supervises stateful `pty` (`forkpty`) and `pipe` child sessions over a private `FD_CLOEXEC` length-framed control and ownership pipe: worker exit or crash closes that pipe, causing the supervisor to reap supervised child process groups cleanly (noting grandchildren that intentionally detach can leave those groups). The worker drains children on normal exit and the app reaps its child.

## Data and execution

Keep Bot identity separate from conversation and activity execution IDs. A chat can continue while an activity waits for a targeted answer. The worker routes answers, cancellation and results to the captured execution. Preserve material snapshots and receipts across restart. See the [product methods](product-protocol.md#methods) and [supported profile](product-protocol.md#current-profile-admission).

## Build and verification

The [runtime pin](../../.github/pi-runtime.json) identifies Node and Pi independently. The worker package lock and build tool bind the shipped code and runtime to their inputs, compile `internal/pi/worker/pty-supervisor.c` from relative checkout sources (`arm64`, `MACOSX_DEPLOYMENT_TARGET=13.0`, `-fno-ident`, retaining Apple linker deterministic `LC_UUID` required by macOS 27 `dyld`) into `bin/nagi-pty-supervisor`, and ad-hoc sign it (`dev.nagi.pty-supervisor`) before hashing `manifest.json`. The app assembler validates `minimum_os`, `lipo`, and helper signature, signs the app without altering the nested payload manifest, and writes a manifest alongside it. [Build instructions](../../README.md#development) list the commands and output paths.

Exercise the signed app on macOS with Node absent from its search path. Verify that its child uses the bundled executable, that the app opens an isolated profile and that normal quit reaps the child. For execution behavior, use the pinned worker with a controlled local provider, check each command target and count requests across restart.
