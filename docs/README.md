# Nagi documentation

Updated: 2026-11-14.

Nagi has an independent repository and root Go module. The [root README](../README.md) covers preview installation, first connection, file snapshots and recovery, followed by contributor build, test, packaging and data-compatibility instructions.

Current work is tracked in the [migration and ownership index](https://github.com/HeimaoLST/Nagi/issues/2), [product roadmap](https://github.com/HeimaoLST/Nagi/issues/5), [complete-product acceptance](https://github.com/HeimaoLST/Nagi/issues/7) and [Nagi MVP milestone](https://github.com/HeimaoLST/Nagi/milestone/2). Older Saki issue and source links below retain their historical provenance.

- [Product requirements](requirements/desktop.md): current contacts/issues, consumer configuration and recovery contracts, and complete MVP acceptance criteria.
- [Current visual contract](design/desktop.md#qoder-match-design-20261113): the shared Qoder-derived typography, spacing, controls and icon roles.
- [Workbench design foundation](design/desktop-workbench.md): earlier layout and interaction rules, subject to the newer requirements and visual contract.
- [Implementation and evidence](design/desktop.md): current repository boundary followed by dated implementation and acceptance history.
- [Source import](design/source-import.json): byte-level import provenance from Saki candidate `00298ae` with original Git history retained.
- [Experience-to-behavior research](https://github.com/HeimaoLST/Saki/blob/dev/docs/design/experience-to-behavior.md): a historical proposal, not a required Nagi execution layer.
- [Saki Runtime API](https://github.com/HeimaoLST/Saki/blob/dev/docs/design/runtime-api.md): authoritative version 1 service contract; Nagi consumes it without copying its execution implementation.

Implementation facts, author reports, design proposals and unverified behavior remain distinct. The repository split does not complete native/IME/real-model/user acceptance; historical SHA, package and CI evidence applies only to its recorded inputs. Older `desktop/` source paths refer to the original Saki layout; current Go files live at this repository root.
