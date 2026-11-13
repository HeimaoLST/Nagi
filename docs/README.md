# Nagi documentation

Updated: 2026-11-10.

Nagi now has an independent repository and root Go module. The [root README](../README.md) is the current build, test, packaging and data-compatibility entry point.

Current work is tracked in the [migration and ownership index](https://github.com/HeimaoLST/Nagi/issues/2), [product roadmap](https://github.com/HeimaoLST/Nagi/issues/5), [complete-product acceptance](https://github.com/HeimaoLST/Nagi/issues/7) and [Nagi MVP milestone](https://github.com/HeimaoLST/Nagi/milestone/2). Older Saki issue and source links below retain their historical provenance.

- [Product requirements](requirements/desktop.md): contacts, issues, requirements and the historical MVP acceptance criteria.
- [Workbench design](design/desktop-workbench.md): current visual and interaction rules.
- [Implementation and evidence](design/desktop.md): current repository boundary followed by dated implementation and acceptance history.
- [Source import](design/source-import.json): byte-level import provenance from Saki candidate `00298ae` with original Git history retained.
- [Experience-to-behavior research](https://github.com/HeimaoLST/Saki/blob/dev/docs/design/experience-to-behavior.md): a historical proposal, not a required Nagi execution layer.
- [Saki Runtime API](https://github.com/HeimaoLST/Saki/blob/dev/docs/design/runtime-api.md): authoritative version 1 service contract; Nagi consumes it without copying its execution implementation.

Implementation facts, author reports, design proposals and unverified behavior remain distinct. The repository split does not complete native/IME/real-model/user acceptance; historical SHA, package and CI evidence applies only to its recorded inputs. Older `desktop/` source paths refer to the original Saki layout; current Go files live at this repository root.
