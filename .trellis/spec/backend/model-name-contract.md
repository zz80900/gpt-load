# Model Name Contract

> How a client-supplied model name becomes a routable target, which of the
> three name sets each consumer is allowed to read, and how the counts reported
> beside the association rows are defined.

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

### Count contract: the numbers beside the association rows

`GetUpstreamModelDetail` returns association rows plus four counts, and
`web/src/app/resources/models.ts` asserts exact relations between all of them in
a single `if`. Each count answers a different question; conflating them is what
produced the defect this section replaces.

| Field | Counts | Definition site |
| --- | --- | --- |
| `price.reference_count` | Distinct `(group, client-visible name)` pairs | `buildPriceReferenceSnapshot` (`internal/control/price_reconcile.go`) |
| `price.reference_group_count` | Distinct groups across those pairs | same function |
| `associations.length` | The same pairs, materialised as rows | `GetUpstreamModelDetail` (`internal/control/project_model_collection.go`) |
| `client_model_count` | Distinct client-visible names, de-duplicated **across groups** | same site (`len(clientModels)`) |
| `group_count` | Distinct groups over the association rows | same site (`len(groupIDs)`) |

`reference_count` is **not** a name count. The phrase "how many client-visible
names reference this upstream model" describes `client_model_count`;
`reference_count` is the association count, and it equals `associations.length`
even when one name is reachable through several groups. The two numbers coincide
only when every group holds an identical name set — precisely the case in which
a mistake here stays invisible.

> **Warning**: `reference_count` is derived per request and never persisted, so
> changing its definition needs no migration.

#### One key, one definition

`reference_count` equals `associations.length` because both are keyed by the same
function, and that function has exactly one definition:

```go
// internal/control/price_reconcile.go
func priceAssociationKey(groupID uint, clientModel string) string {
    return fmt.Sprintf("%d\x00%s", groupID, clientModel)
}
```

`reference_group_count` and `group_count` agree by a weaker mechanism: two
separate visit loops that filter on the same pair (`channel_id`, `model.id`) and
de-duplicate on the same value (`group.id`). They are correct today, but nothing
structural holds them together — change either filter and check both.

#### Regression baseline

Measured through `GetUpstreamModelDetail`; `entries` is the value
`reference_count` reported before the definition changed, i.e. how many config
rows matched. `reference_group_count` equals `group_count` in every row.

| Configuration | `reference_count` | `associations` | `client_model_count` | `group_count` | entries |
| --- | --- | --- | --- | --- | --- |
| `[{"id":"x"}]` | 1 | 1 | 1 | 1 | 1 |
| `[{"id":"x","aliases":["client-a"]}]` | 2 | 2 | 2 | 1 | 1 |
| `[{"id":"x","aliases":["client-a[1M]"]}]` | 3 | 3 | 3 | 1 | 1 |
| `[{"id":"x","alias":"client-a"}]` — legacy singular field | 2 | 2 | 2 | 1 | 1 |
| `[{"id":"x[1M]"}]` — suffix on the upstream id itself | 2 | 2 | 2 | 1 | 1 |
| `[{"id":"x","aliases":["client-a"]},{"id":"x","aliases":["client-b"]}]` | 3 | 3 | 3 | 1 | 2 |
| `[{"id":"x"},{"id":"x"}]` — repeated row, no aliases | 1 | 1 | 1 | 1 | 2 |
| two groups, `x` configured in each | 2 | 2 | 1 | 2 | 2 |

The last two rows are the sharpest probes. Several rows sharing one upstream id
are **supported configuration, not dirty data**: `normalizeGroupModels` reads
them as "one model, several names" and deliberately lets them through (see the
comment in `internal/control/group_write.go` and the permanent case in
`group_models_test.go`). Under entry-counting they reported 2 against 1
association — the exact drift this contract exists to prevent.

#### Wrong vs Correct

##### Wrong

```go
// The count and the list agree only while two hand-written literals stay in
// sync. Nothing enforces it, and the pair drifts on the first edit.
key := fmt.Sprintf("%d\x00%s", group.ID, clientModel)      // snapshot side
key := fmt.Sprintf("%d\x00%s", group.row.ID, clientModel)  // detail side
```

##### Correct

```go
// Both call sites share one definition.
key := priceAssociationKey(group.ID, clientModel)
```

