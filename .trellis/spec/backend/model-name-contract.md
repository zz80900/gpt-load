# Model Name Contract

> How a client-supplied model name becomes a routable target, which of the
> three name sets each consumer is allowed to read, and how the counts reported
> beside the association rows are defined.

---

## Scenario: Touching anything that consumes a model name

### 1. Scope / Trigger

A "model name" in this codebase is not one set — it is five, with different
audiences and different shapes. Reading the wrong one either breaks a frontend
hard assertion, publishes a pattern as if it were a model, or silently drops a
route. Use this spec when you add, remove, or reformat a name set, or when a
client-visible name must become routable.

The split only became load-bearing once wildcard aliases arrived. Before that,
"in the routing index" and "client-visible" were the same question.

| Set | Owner | Audience | Shape |
| --- | --- | --- | --- |
| `ExternalModelNames` | `internal/state/snapshot.go` | The wire contract: `client_models` in `GetGroupModels`, which `groups.ts` asserts item by item | Exactly `[id, ...aliases]`. **Derived names must never appear here, and patterns must not be filtered out of it.** |
| `RoutableModelNames` | `internal/state/snapshot.go` | Conflict detection only: write path (`normalizeGroupModels`) and compile time (`validateCompileInput`). It does **not** feed the routing index or any display surface | `ExternalModelNames` with each item immediately followed by its context-suffix base, de-duplicated. Patterns appear verbatim |
| Concrete filters | `internal/state/snapshot.go` | Every display/selection surface: group-options candidates, model page, price counts, dashboard, client-config generator | `ConcreteModelNames` / `ConcreteRoutableModelNames` — the two sets above minus wildcard patterns. **This is the set `/v1/models` agrees with**, not `RoutableModelNames`. |
| `ExecutionCandidates` / `ExecutionRouteCatalog` keys | `internal/state/snapshot.go` | Routing index (exact lookup), `/v1/models` listing | The **concrete** names only, taken from `ConcreteRoutableModelNames`. A name containing `*` is never a key here. |
| `ExecutionPatterns` / `ExecutionRoutePatterns` | `internal/state/snapshot.go` | Routing index (wildcard fallback) | The wildcard patterns only, each carrying the targets that claim it. |

For any configuration without a context suffix and without a wildcard alias, the
first two sets are **item-for-item identical**. That equivalence is the
regression signal: such a model must behave exactly as it did before suffix
support existed.

### 2. Signatures

```
Go   state.ExternalModelNames(model ModelConfig) []string
     state.RoutableModelNames(model ModelConfig) []string
     state.ConcreteModelNames(model ModelConfig) []string
     state.ConcreteRoutableModelNames(model ModelConfig) []string
     state.ModelPatternIndex  // per (protocol, operation) -> patterns sorted by specificity
     modelname.Base(name string) string
     modelname.Allows(allowed map[string]struct{}, name string) bool
     modelname.IsPattern(name string) bool
     modelname.Match(pattern, name string) bool
     modelname.PatternSpecificity(pattern string) (prefixLen, literalCount int)
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
  it, so every call site must keep its own `len(filters) > 0` guard. `Allows`
  has **no** wildcard semantics: a `claude-*` entry in an allowlist never matches,
  which is why patterns are kept out of the candidate pickers.
- **Registration is the single source of routability, but it is now two channels.**
  A name reaches routing either as an exact index key or as a pattern in
  `ExecutionPatterns` / `ExecutionRoutePatterns`; `appendExecutionTargets` is the
  only place that decides which. `/v1/models` iterates the **exact** index keys,
  so a pattern never appears in the list — this is the opposite of the suffix
  case, where registering the derived name made it visible for free.
- **The only wildcard match site is `evaluateTargets`.** Exact hit wins and
  short-circuits: a non-empty `byModel[modelKey]` returns immediately and never
  consults patterns, and pattern targets are never merged into an exact hit. The
  fallback is guarded by `query.externalModel != nil`, so `NoModelRouteKey`
  (resource operations) can never be captured by a pattern that matches the empty
  string. Adding a second match site in the gateway would let the patrol path
  disagree with the data plane.
- **Pattern precedence is fixed at build time.** Patterns are sorted by
  `lessModelPattern` (longest literal prefix, then most literal characters, then
  byte order) and the first match wins outright. Nothing at request time sorts,
  allocates, or consults map iteration order — a non-deterministic tie-break
  surfaces as routing that flaps between restarts.
- **`*` matches any byte sequence, including empty and including a context
  suffix.** `claude-*` therefore already matches `claude-opus-5[1m]`; the
  `claude-*[1m]` → `claude-*` derivation covers the un-suffixed request, not the
  other way round.

### 4. Validation & Error Matrix

Name-collision detection exists in three places that must **not** all agree:

| Location | Nature | Name set | Behaviour on derived-name collision |
| --- | --- | --- | --- |
| `internal/control/group_write.go` (`normalizeGroupModels`) | Write path | **Routable** | Rejects the save with `MODEL_NAME_CONFLICT` |
| `internal/state/snapshot.go` (`validateCompileInput`) | Compile time | **Routable** | Refuses to publish the new snapshot; the running configuration keeps serving |
| `internal/control/group_collection.go` (`validateGroupCollectionModels`) | Read path | **External** | Passes. Wrapping it in `ErrInternalServer` would 500 the group list and the options endpoint, leaving the user no screen on which to fix the conflict |

Collision is decided by **exact string equality within one group**, and that rule
covers patterns without a special case. The three outcomes it produces are all
intended:

| Pattern situation | Outcome | Why |
| --- | --- | --- |
| Same pattern, two different upstream IDs | Conflict | The request would resolve by candidate ordering rather than configuration |
| Same pattern, two rows of the same upstream ID | Allowed | Pre-existing "one model, several names" configuration |
| `claude-*` next to `claude-opus-5`, or `claude-*` next to `claude-sonnet-*` | Allowed | Different strings. Exact-match priority and pattern specificity decide, and rejecting would forbid the workflow the feature exists for |

Do **not** extend the check into a pattern-vs-exact prefix-overlap test.

The read path stays on the external set precisely so that pre-upgrade data
containing both an alias `xxxx[1M]` and another entry whose `id` is `xxxx`
remains visible and repairable. The cost is a narrow window where a save
succeeds but the snapshot will not compile — recoverable, because `Publish`
does not swap in a failed snapshot.

| Condition | Behaviour |
| --- | --- |
| Collision between two explicit names | `duplicate external model %q` |
| Collision where one side is derived | Error must name the derived source and the current owner — the user never typed that name and cannot find it otherwise |
| Collision between two patterns | `duplicate external model "claude-*"` — the user typed that exact string, so the plain form is the locatable one |
| Compile fails on stored data | No new snapshot published; the old configuration keeps serving |
| Alias is exactly `[1M]` | Accepted; no derived name, no empty index key |
| Alias contains `*` | Accepted and routed as a pattern. A bare `*` is legal and captures every otherwise-unrouted name in the group |
| Derived name equals an explicit name in the same model | Registered once (the index `claimed` map de-dupes, and patterns go through the same de-dup) |

### 5. Good / Base / Bad Cases

- **Good**: alias `claude-deepseek-flash[1M]` on upstream `deepseek-flash`.
  `/v1/models` lists `deepseek-flash`, `claude-deepseek-flash[1M]`,
  `claude-deepseek-flash`; all three route to the same target.
- **Good (wildcard)**: alias `claude-*[1m]` on upstream `deepseek-flash`.
  `/v1/models` lists only `deepseek-flash` and clients never see the pattern, but
  `claude-opus-5`, `claude-sonnet-5` and `claude-opus-5[1m]` all route to it.
- **Base**: a configuration with no suffix. `RoutableModelNames` and
  `ExternalModelNames` return the same slice and every downstream assertion keeps
  its original value. With no wildcard alias the two concrete filters are the
  identity function, and both pattern indexes are empty.
- **Bad**: a request arrives with a name that is neither an exact index key nor
  matched by any pattern. It lands on `ReasonNoRouteTarget` and is written out as
  a 503 `no_available_candidate` — the same result as before wildcards existed.
- **Silent change**: a stored `*` alias that predates this feature used to be dead
  weight (it matched nothing and was advertised verbatim in `/v1/models`). It is
  now live routing configuration.

### 6. Tests Required

| Test | Assertion point |
| --- | --- |
| `internal/modelname/modelname_test.go` | `Base` is identity without a suffix; both casings strip; `[1M]` alone is not stripped to empty; a suffix mid-name is not stripped; `Allows` covers both directions and never matches on an empty set; `Match` handles prefix/suffix/middle/multiple `*`, empty names and suffixed request names; `PatternSpecificity` ranks prefix before literal count |
| `internal/state/snapshot_test.go` | Index holds both the suffixed name and its base, pointing at one upstream; derived-vs-explicit collision fails `Compile` naming the derived source; suffix-free models produce byte-identical name sets; no `*` key ever reaches `ExecutionCandidates` / `ExecutionRouteCatalog`; patterns aggregate across groups, sort by specificity regardless of group order, and compile byte-identically on repeat (the `Manager.Matches` contract) |
| `internal/control/group_models_test.go` | After saving a suffixed alias, a re-read still yields the original `aliases` and `client_models`; a base name owned by another entry yields `MODEL_NAME_CONFLICT`; a wildcard alias survives the round trip verbatim, is absent from the exact index, and is absent from the group-options candidate set; same-pattern-cross-ID conflicts while same-ID and overlapping patterns do not |
| `internal/scheduler/inspect_test.go`, `internal/gateway/models_test.go` | An allowlist holding only `xxxx[1M]` admits a request for `xxxx` and lists both; an unrelated allowlist still yields `ReasonModelFiltered`; a pattern routes names absent from the configuration; an exact key still wins; overlapping patterns resolve by specificity; `NoModelRouteKey` is never pattern-matched |
| `internal/gateway/dialects_integration_test.go` | End to end: a request name that appears nowhere in the configuration still returns 200, the upstream receives the configured upstream ID, the client gets its own requested name back, and `/v1/models` omits the pattern |
| `internal/control/project_model_collection_test.go`, `internal/control/home_test.go` | Pattern names are absent from the model page, its association rows, `reference_count` / `client_model_count`, and the dashboard name set — all of which must stay mutually consistent |
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

// A pattern registered as an exact key. /v1/models iterates these keys, so the
// pattern is advertised to clients as if it were a real model — and it can never
// be matched by an exact lookup, because no client sends "claude-*".
func appendExecutionTargets(index ExecutionCandidateIndex, group GroupConfig) error {
    for _, name := range RoutableModelNames(model) {
        appendExecutionTarget(index, clientProtocol, operation, name, target)
    }
}

// A second wildcard match site. The patrol path reads a *different* index
// (ExecutionRouteCatalog) through the same evaluateTargets, so a fallback added
// in the gateway makes route inspection disagree with real routing.
func (h *handler) resolveModel(name string) {
    targets, ok := byModel[name]
    if !ok && strings.Contains(name, "*") { /* ... */ }
}
```

> **History**: an earlier revision of this spec listed "a fallback in the lookup
> path" as Wrong. That conclusion is correct for the **finite** suffix derivation —
> the base name is computable at build time, so registering it is strictly better
> than special-casing the lookup. It is **wrong** for `*`: a pattern's match set is
> unbounded and cannot be enumerated into index keys, so a lookup-time match is the
> only workable design. The rule that survives is narrower: the fallback may exist,
> but only inside `evaluateTargets`.

#### Correct

```go
// Two sets, two audiences; the index registration is the one that matters.
func RoutableModelNames(model ModelConfig) []string { /* ... */ }

// Write path and compile time share the routable set...
names := state.RoutableModelNames(state.ModelConfig{ID: id, Aliases: aliases})

// ...and the one registration point splits it into two channels.
if modelname.IsPattern(registration.name) {
    appendModelPattern(patterns, clientProtocol, operation, registration.name, routeTarget)
    continue
}
appendExecutionTarget(index, clientProtocol, operation, registration.name, routeTarget)

// The one match site: exact first, patterns only on a miss, and never for
// model-less resource operations.
routes := byModel[modelKey]
if len(routes) == 0 && query.externalModel != nil {
    routes = matchModelPattern(patterns[query.clientProtocol][query.operation], modelKey)
}

// Display surfaces take the concrete set; the contract set stays untouched.
names := state.ConcreteRoutableModelNames(state.ModelConfig{ID: model.ID, Aliases: model.Aliases})
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
| `[{"id":"x","aliases":["claude-*[1m]"]}]` — wildcard only | 1 | 1 | 1 | 1 | 1 |
| `[{"id":"x","aliases":["claude-*[1m]","client-a"]}]` | 2 | 2 | 2 | 1 | 1 |

The wildcard pair is its own probe. A pattern is **not** a client-visible name: it
contributes no association row and no count, even though it is what makes
`claude-opus-5` reach this upstream. Counting it would inflate every one of these
numbers and, worse, put the literal string `claude-*` in front of an operator who
was told patterns are internal.

The two rows above it — the repeated row and the cross-group row — are the
sharpest probes. Several rows sharing one upstream id
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

