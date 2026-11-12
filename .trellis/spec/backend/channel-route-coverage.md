# Channel Route Coverage

> A channel that claims a client protocol must declare every operation that protocol's
> dialect can emit. One missing `(protocol, operation)` pair silently produces a
> zero-attempt 503 that looks like capacity exhaustion.

---

## Scenario: Adding or reviewing a channel's route table

### 1. Scope / Trigger

Trigger when you add a channel module, add a client-protocol operation to a dialect, or
diagnose a `503 no_available_candidate` whose request log row has `0ms`, an empty group,
and an empty upstream credential. Those three fields together mean **no attempt was ever
made**, not that every credential was busy or cooling down.

### 2. Signatures

```
Go   spec.NewRoute(clientProtocol protocol.Protocol, operation execution.Operation, mode RouteMode) spec.Route
     target.Operations(clientProtocol) []execution.Operation     // descriptor client -> allowed ops
     target.ModeForModel(clientProtocol, operation, upstreamModelID) (RouteMode, bool)
     scheduler.evaluateTargets(snapshot, index, patterns, query) ([]targetDecision, ReasonCode, error)
JSON no direct JSON shape; routes compile into ExecutionCandidates[protocol][operation][externalName]
```

Client endpoints that map to operations (Anthropic dialect, `internal/dialect/anthropic.go`):

| Endpoint | Operation |
| --- | --- |
| `POST /v1/messages` | `chat_completion` |
| `POST /v1/messages/count_tokens` | `count_tokens` |

### 3. Contracts

- Route coverage is declared per `(channel, clientProtocol, operation)`. The scheduler
  reads `index[clientProtocol][operation]`; a missing operation key is **not** a
  fallback to another operation — it is `ReasonOperationUnsupported` with zero targets.
- Claude Code calls `/v1/messages/count_tokens` every turn. Any Anthropic-capable
  channel intended for Claude Code must declare `OperationCountTokens`; otherwise every
  turn emits a 503 alongside its successful streaming request.
- Native multi-protocol gateways (New API, CLIProxyAPI) pass `count_tokens` through with
  `RouteNative`; `bifrost.prepare` selects native passthrough because
  `providerSupportsPassthrough(ProviderMultiProtocolGateway)` is true. Converted
  count-token targets are only implemented for provider kinds in `countTokensTypedTarget`.
- `handler.executeAttempts` maps only `model_required_by_filter` and
  `advisor_model_unavailable` to their own reasons; every other static scheduler reason
  falls through to `503 no_available_candidate`. Fix the route, not the error mapping,
  unless a separate decision changes the public error contract.

### 4. Validation & Error Matrix

| Condition | Scheduler reason | Client result |
| --- | --- | --- |
| `(protocol, operation)` declared, model routes | none | attempt proceeds |
| operation missing from index | `operation_unsupported` | 503 `no_available_candidate`, 0 attempts |
| operation declared, model name unknown | `no_route_target` | 503 `no_available_candidate`, 0 attempts |
| candidates exist but all in cooldown | `credential_cooldown` / `model_cooldown` | 429 `upstream_rate_limited` with `Retry-After` |
| all credentials blacklisted | `credential_blacklisted` | 503 `no_available_candidate` |

A 503 with an empty group column is not a rate-limit symptom; check the route table first.

### 5. Good / Base / Bad Cases

- **Good**: `newapi` declares `(Anthropic, count_tokens) -> RouteNative`; a Claude Code
  turn's count request resolves to the group's upstream model and is passed to
  `{base_url}/v1/messages/count_tokens`.
- **Base**: `(Anthropic, chat_completion)` unchanged; other channels without the route
  keep their current behavior.
- **Bad**: `newapi` declares only `chat_completion` and `probe`; every
  `/v1/messages/count_tokens` lands on `operation_unsupported` and returns 503 with no
  group and no credential in the request log.

### 6. Tests Required

| Test | Assertion point |
| --- | --- |
| `internal/channel/newapi_module_test.go` | Expected route map contains `(Anthropic, count_tokens) -> RouteNative` |
| `internal/channel/route_golden_test.go` | Digest changes only by the intended `newapi\|anthropic\|count_tokens\|native` line |
| `internal/scheduler/inspect_test.go` `TestInspectRoutesAnthropicCountTokensOnNewAPI` | `Inspect` returns one included group whose upstream model is the configured ID, not the alias |

### 7. Wrong vs Correct

#### Wrong

```go
// Client protocol claimed, dialect operation not declared. Claude Code's per-turn
// count request has no index entry, so the gateway answers 503 with no attempt.
spec.NewRoute(protocol.Anthropic, execution.OperationChatCompletion, execution.RouteNative),
spec.NewRoute(protocol.Anthropic, execution.OperationProbe, execution.RouteNative),
```

#### Correct

```go
// Count tokens follows the same native route as chat; the executor already supports
// Anthropic count_tokens passthrough for multi-protocol gateways.
spec.NewRoute(protocol.Anthropic, execution.OperationChatCompletion, execution.RouteNative),
spec.NewRoute(protocol.Anthropic, execution.OperationProbe, execution.RouteNative),
spec.NewRoute(protocol.Anthropic, execution.OperationCountTokens, execution.RouteNative),
```
