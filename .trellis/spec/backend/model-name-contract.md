# Model Name Contract

> How a client-supplied model name becomes a routable target, and which of the
> three name sets each consumer is allowed to read.

---

## Scenario: Touching anything that consumes a model name

### 1. Scope / Trigger

A "model name" in this codebase is not one set — it is three, with different
audiences and different shapes. Reading the wrong one either breaks a frontend
hard assertion or silently drops a route. Use this spec when you add, remove, or
reformat a name set, or when a client-visible name must become routable.

| Set | Owner | Audience | Shape |
| --- | --- | --- | --- |
| `ExternalModelNames` | `internal/state/snapshot.go` | Client-visible configuration: `client_models`, the group-options picker, frontend selectors | Exactly `[id, ...aliases]`. **Derived names must never appear here.** |
| `RoutableModelNames` | `internal/state/snapshot.go` | Routing index (`ExecutionCandidates` + `ExecutionRouteCatalog`), write-path and compile-time conflict detection, model page | `ExternalModelNames` with each item immediately followed by its context-suffix base, de-duplicated |
| Group-options candidate set | `internal/control/group_options.go` | "Which models may I put in this group" selector | Same as `ExternalModelNames`. Derived names are deliberately excluded. |

For any configuration without a context suffix the first two sets are
**item-for-item identical**. That equivalence is the regression signal: a
suffix-free model must behave exactly as it did before suffix support existed.

### 2. Signatures

```
Go   state.ExternalModelNames(model ModelConfig) []string
     state.RoutableModelNames(model ModelConfig) []string
     modelname.Base(name string) string
     modelname.Allows(allowed map[string]struct{}, name string) bool
     var modelname.ContextSuffixes = [...]string{"[1M]", "[1m]"}
JSON client_models: [id, ...aliases]            // groups.ts asserts this exactly
```

`internal/modelname` deliberately imports nothing from `gpt-load/internal`.
`state` depends on `parameteroverride`, so a suffix helper living in `state`
would be an import cycle the moment `parameteroverride` needs it.

### 3. Contracts

- **Context suffixes are runtime semantics, never persisted.** `[1M]` / `[1m]` is
  a client-side context-window marker: Claude Code reads `xxxx[1M]` from
  `/v1/models` to enable the large window, then sends `xxxx`. Saving an alias
  `xxxx[1M]` must leave `aliases` byte-for-byte unchanged.
- **`Base` is identity for suffix-free names**, so every consumer of
  `RoutableModelNames` is a no-op for existing configurations.
- **`Base` never returns empty.** A name must be strictly longer than the suffix
  to be stripped, so the alias `[1M]` alone produces no derived name and no empty
  index key.
- **Derived names follow their source item** (`[id, idBase, alias1, alias1Base,
  ...]`), so the model page renders them in pairs. `id` stays first — the
  frontend asserts that position.
- **`Allows` is bidirectional**: a suffixed request matches its base in the
  allowlist, and a base request matches a suffixed allowlist entry. An empty
  allowlist is the caller's concern; `Allows` deliberately does not special-case
  it, so every call site must keep its own `len(filters) > 0` guard.
- **The routing index is the single registration point.** `/v1/models` iterates
  the index keys, so registering derived names there makes them appear in the
  list for free. Do not add a fallback in the lookup path instead — the patrol
  path shares that lookup and would need the same fallback duplicated.

### 4. Validation & Error Matrix

Name-collision detection exists in three places that must **not** all agree:

| Location | Nature | Name set | Behaviour on derived-name collision |
| --- | --- | --- | --- |
| `internal/control/group_write.go` (`normalizeGroupModels`) | Write path | **Routable** | Rejects the save with `MODEL_NAME_CONFLICT` |
| `internal/state/snapshot.go` (`validateCompileInput`) | Compile time | **Routable** | Refuses to publish the new snapshot; the running configuration keeps serving |
| `internal/control/group_collection.go` (`validateGroupCollectionModels`) | Read path | **External** | Passes. Wrapping it in `ErrInternalServer` would 500 the group list and the options endpoint, leaving the user no screen on which to fix the conflict |

The read path stays on the external set precisely so that pre-upgrade data
containing both an alias `xxxx[1M]` and another entry whose `id` is `xxxx`
remains visible and repairable. The cost is a narrow window where a save
succeeds but the snapshot will not compile — recoverable, because `Publish`
does not swap in a failed snapshot.

| Condition | Behaviour |
| --- | --- |
| Collision between two explicit names | `duplicate external model %q` |
| Collision where one side is derived | Error must name the derived source and the current owner — the user never typed that name and cannot find it otherwise |
| Compile fails on stored data | No new snapshot published; the old configuration keeps serving |
| Alias is exactly `[1M]` | Accepted; no derived name, no empty index key |
| Derived name equals an explicit name in the same model | Registered once (the index `claimed` map de-dupes) |

### 5. Good / Base / Bad Cases

- **Good**: alias `claude-deepseek-flash[1M]` on upstream `deepseek-flash`.
  `/v1/models` lists `deepseek-flash`, `claude-deepseek-flash[1M]`,
  `claude-deepseek-flash`; all three route to the same target.
- **Base**: a configuration with no suffix. `RoutableModelNames` and
  `ExternalModelNames` return the same slice and every downstream assertion keeps
  its original value.
- **Bad**: a request arrives with a name absent from the index. The index is an
  exact map lookup with no fallback, so the request lands on
  `ReasonNoRouteTarget` and is written out as a 503 `no_available_candidate`.

### 6. Tests Required

| Test | Assertion point |
| --- | --- |
| `internal/modelname/modelname_test.go` | `Base` is identity without a suffix; both casings strip; `[1M]` alone is not stripped to empty; a suffix mid-name is not stripped; `Allows` covers both directions and never matches on an empty set |
| `internal/state/snapshot_test.go` | Index holds both the suffixed name and its base, pointing at one upstream; derived-vs-explicit collision fails `Compile` naming the derived source; suffix-free models produce byte-identical name sets |
| `internal/control/group_models_test.go` | After saving a suffixed alias, a re-read still yields the original `aliases` and `client_models`; a base name owned by another entry yields `MODEL_NAME_CONFLICT` |
| `internal/scheduler/inspect_test.go`, `internal/gateway/models_test.go` | An allowlist holding only `xxxx[1M]` admits a request for `xxxx` and lists both; an unrelated allowlist still yields `ReasonModelFiltered` |
| `internal/parameteroverride/rules_test.go` | A rule written for `xxxx[1M]` fires for a request of `xxxx`, and vice versa; suffix-free cases keep their original expectations |

### 7. Wrong vs Correct

#### Wrong

```go
// Derived names leak into the client-visible set: the frontend asserts
// client_models == [id, ...aliases] and rejects the whole response.
func ExternalModelNames(model ModelConfig) []string {
    names := append([]string{model.ID}, model.Aliases...)
    // ...append modelname.Base(name) for each...
    return names
}

// Fallback in the lookup path instead of the index. The patrol path shares this
// lookup, so it needs the same fallback — and the list endpoint still would not
// show the base name.
func (q *inspectQuery) evaluateTargets() {
    targets, ok := byModel[*q.externalModel]
    if !ok {
        targets, ok = byModel[modelname.Base(*q.externalModel)]
    }
    // ...
}
```

#### Correct

```go
// Two sets, two audiences; the index registration is the one that matters.
func RoutableModelNames(model ModelConfig) []string { /* ... */ }

// Write path and compile time share the routable set...
names := state.RoutableModelNames(state.ModelConfig{ID: id, Aliases: aliases})
```

### Known cross-layer defect

`GetUpstreamModelDetail` counts two different things. `price.reference_count`
comes from `buildPriceReferenceSnapshot` (`internal/control/price_reconcile.go`),
which increments **once per config entry**; `associations` is built **once per
(client-visible name, group)** pair. `web/src/app/resources/models.ts` asserts
they are equal, so the upstream-model detail page fails to load whenever a
single entry owns more than one name.

This is pre-existing and **unrelated to suffixes**. Measured on the current
build: a single *plain* alias already yields `reference_count = 1` against
`associations.length = 2`. Only an entry with no alias at all satisfies the
assertion — it was written for a world where one config entry owned exactly one
name.

Derived names do not change pass/fail; they only raise the name count. The one
shape that crosses the boundary is an upstream id that itself ends in `[1M]`
with no alias, which goes from 1:1 (passing) to 1:2.

The other count invariants in that assertion — `reference_group_count ==
group_count` and `client_model_count == distinct client models` — hold in every
shape, so a fix only has to reconcile one field. `reference_count` counts config
entries where the assertion expects client-visible names; both are meant to
express "how many client-visible names reference this upstream model".

