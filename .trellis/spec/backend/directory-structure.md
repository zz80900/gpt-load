# Directory Structure

> How backend code is organized in this project.

---

## Overview

<!--
Document your project's backend directory structure here.

Questions to answer:
- How are modules/packages organized?
- Where does business logic live?
- Where are API endpoints defined?
- How are utilities and helpers organized?
-->

(To be filled by the team)

---

## Directory Layout

```
<!-- Replace with your actual structure -->
src/
├── ...
└── ...
```

---

## Module Organization

<!-- How should new features/modules be organized? -->

(To be filled by the team)

---

## Naming Conventions

<!-- File and folder naming rules -->

(To be filled by the team)

---

## Dependency Direction

`internal/` packages form a downward-only graph. When two packages need the same
logic but only one import direction is legal, the shared logic moves into a new
**leaf package with zero `gpt-load/internal` imports** — not into whichever
package happens to already exist.

**Real case**: `state` imports `parameteroverride`, so `parameteroverride` can
never import `state`. Context-suffix canonicalization is needed by both, so it
lives in `internal/modelname`, which imports only `strings`. Placing the helper
in `state` would have closed the cycle. See
[Model Name Contract](./model-name-contract.md).

Before adding a helper to an existing package, check whether the package you
want to serve already sits below you:

```bash
go list -deps ./internal/<pkg> | grep gpt-load/internal/
```

If the new dependency would point upward, create a leaf package instead.

---

## Examples

<!-- Link to well-organized modules as examples -->

(To be filled by the team)
