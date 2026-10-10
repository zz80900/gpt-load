# Converted Route Field Semantics

> What survives a protocol conversion, what silently collapses, and where a
> field must be mapped explicitly. Learned while implementing fast-mode
> passthrough on converted routes.

---

## Scenario: Changing field handling in `internal/execution/bifrost/converted.go`

### 1. Scope / Trigger

`buildConvertedResponsesRequest` rebuilds the upstream request from the client
payload through SDK structures. Any client field that has no explicit mapping
is dropped, and a field that shares a name with an SDK field can be silently
re-interpreted with different protocol semantics. Read this before adding,
removing, or renaming a field mapping in that file.

### 2. Contracts

**Client fields are per-protocol semantics, not shared names.**

- Anthropic `speed:"fast"` is the fast-mode carrier (SDK:
  `AnthropicMessageRequest.Speed` → `ExtraParams["speed"]`,
  `providers/anthropic/responses.go:4107`). On the way out, Anthropic itself
  reads it back to restore fast mode (`providers/anthropic/responses.go:4612`).
- Anthropic `service_tier` only has `auto` / `standard_only` semantics
  (`MapAnthropicRequestServiceTierToBifrost`). Any other value (including
  `fast`) collapses to `auto`. Never use it as a fast-mode configuration
  surface for Anthropic clients.
- Upstream OpenAI tiers are `auto/default/flex/priority/ultrafast/provisioned`
  (`schemas/chatcompletions.go`); `fast` is not one of them. The Codex (CPA)
  path maps `speed:"fast" → service_tier:"priority"`; converted routes align
  with that mapping (`applyConvertedFastMode`).

**ExtraParams do not reach the upstream body by default.**

- `Params.ExtraParams` is merged into the upstream JSON only with
  `BifrostContextKeyPassthroughExtraParams` set (`providers/utils/utils.go`),
  which `newSDKContext` enables for exactly two shapes: DeepSeek + Anthropic
  chat and Probe. Everywhere else, a field that only lives in `ExtraParams`
  must be lifted into a typed request field by explicit code in the converted
  builder, or it never leaves the process.

**`buildConvertedResponsesRequest` is shared by native routes.**

- RouteConverted always goes through it, but `executor.go` also routes some
  RouteNative shapes to it (DeepSeek Anthropic compatibility endpoints and
  providers without native passthrough). DeepSeek's native Anthropic route
  relies on `ExtraParams["speed"]` surviving to restore fast mode. New mappings
  must therefore gate on `spec.RouteMode == execution.RouteConverted`; an
  ungated mapping regresses that route (guard test:
  `TestConvertedFastModeLeavesNativeAnthropicRouteUnchanged`).

**Test through the real upstream body.**

- Assert on the serialized body produced by
  `providerUtils.CheckContextAndGetRequestBody` + `openai.ToOpenAIResponsesRequest`,
  not on the intermediate request struct. When the behavior under test is the
  "field does not leak" half, enable
  `BifrostContextKeyPassthroughExtraParams` in the test context — otherwise the
  extras merge is off and the leak assertion cannot fail (mutation-tested in
  `converted_fast_mode_test.go`).

### 3. Signatures

```
Go   buildConvertedResponsesRequest(spec execution.AttemptSpec, provider schemas.ModelProvider) (*schemas.BifrostResponsesRequest, error)
     applyConvertedFastMode(request *schemas.BifrostResponsesRequest)   // speed:"fast" → Params.ServiceTier=priority, clears ExtraParams["speed"]
Test internal/execution/bifrost/converted_fast_mode_test.go
```

### 4. Common mistakes

- Treating `service_tier:"fast"` as a configurable fast switch for Anthropic
  clients — it silently becomes `auto` on the wire.
- Adding a mapping at the top of the converted builder without a RouteConverted
  gate — breaks DeepSeek's native Anthropic fast restoration.
- Asserting "no `speed` in body" while extras passthrough is off — the
  assertion passes for the wrong reason.
