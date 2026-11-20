# Nagi documentation

Start with the [v1 desktop user flows](requirements/desktop.md#v1) for product scope. The [product protocol](design/product-protocol.md) describes the MyGo and TypeScript operations, ownership, and restart behavior that support those flows.

- The [visual contract](design/desktop.md#qoder-complete-layout-20261116), [approved phase 1 corrections](design/ui-checklist.md) and [UI review workflow](design/ui-review.md) govern native UI implementation and review. Phase 2 is a proposal.
- The [Pi integration document](design/pi-integration.md) covers execution ownership, packaging and migration evidence. The [root README](../README.md) describes current user and contributor entry points.
- The [earlier workbench design](design/desktop-workbench.md), [original MVP specification](https://github.com/HeimaoLST/Nagi/blob/3c96704e327a3c4653ee6d17ad607f4a4067543e/docs/requirements/desktop.md) and [source import record](design/source-import.json) explain earlier decisions and data shapes. Use them when tracing provenance or maintaining existing records.

For an implementation or test change, name the current user action or supported data contract it protects. Prior mockups, existing settings fields and historical passing tests establish what existed. A feature that appears only there needs a current product decision before it becomes a v1 requirement.
