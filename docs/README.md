# Nagi documentation

Updated: 2026-11-14.

Nagi has an independent repository and root Go module. The [root README](../README.md) separates earlier preview installation and preserved consumer flows from the current incomplete runtime replacement, contributor build instructions and data-compatibility boundaries. Current CI does not deliver a standalone app.

Current work is tracked in the [migration and ownership index](https://github.com/HeimaoLST/Nagi/issues/2), [product roadmap](https://github.com/HeimaoLST/Nagi/issues/5), [complete-product acceptance](https://github.com/HeimaoLST/Nagi/issues/7) and [Nagi MVP milestone](https://github.com/HeimaoLST/Nagi/milestone/2). Older Saki issue and source links below retain their historical provenance.

- [Product requirements](requirements/desktop.md): current contacts/issues, consumer configuration and recovery contracts, and complete MVP acceptance criteria.
- [Current visual contract](design/desktop.md#qoder-complete-layout-20261116): the shared Qoder-derived typography, spacing, controls and icon roles.
- [UI aesthetic standard and checklist](design/ui-checklist.md): the user-requested aesthetic standard and rendered-page checks. Phase 1 keeps the Qoder base and applies the decided minimum-quality corrections, which supersede the matching Qoder values; phase 2 is a proposed Nagi style direction.
- [Workbench design foundation](design/desktop-workbench.md): earlier layout and interaction rules, subject to the newer requirements and visual contract.
- [Implementation and evidence](design/desktop.md): current repository boundary followed by dated implementation and acceptance history.
- [Source import](design/source-import.json): byte-level import provenance from Saki candidate `00298ae` with original Git history retained.
- [Pi product integration](design/pi-integration.md): active cleanup-first migration tracked in [issue31](https://github.com/HeimaoLST/Nagi/issues/31). The previous Saki service contract remains historical input; the current integration does not authorize Saki development or legacy-history conversion.

The obsolete browser prototype, unused mascot loader, unrelated research stub and Saki-only app packager have been retired. Their original bytes remain in Git history; a browser scene is not a native product implementation.

Implementation facts, author reports, design proposals and unverified behavior remain distinct. The repository split does not complete native/IME/real-model/user acceptance; historical SHA, package and CI evidence applies only to its recorded inputs. Older `desktop/` source paths refer to the original Saki layout; current Go files live at this repository root.
