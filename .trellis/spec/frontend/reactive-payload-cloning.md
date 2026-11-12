# Reactive Payload Cloning

> Vue `ref` values are deep proxies. `structuredClone` throws `DataCloneError` on them, so a
> retry-freezing clone must serialize through JSON instead.

---

## Scenario: Freezing a request body for idempotent retry

### 1. Scope / Trigger

Trigger when code deep-copies a value that may originate from a Vue `ref` / `reactive`
state — typically a submit payload frozen for `Idempotency-Key` retries.

### 2. Signatures

```
TS  structuredClone(value): T                     // throws DataCloneError on Proxy
    JSON.parse(JSON.stringify(value)) as T        // safe for JSON wire payloads
```

### 3. Contracts

- Proxies created by Vue reactivity are not structured-cloneable. Nested values are
  proxied too: reading `model.aliases` from a reactive array yields a proxy array.
- Only clone at the shared submit boundary (`useGroupCreateOperation.begin`), never in
  each caller; callers are allowed to pass reactive state.
- The payload type must be JSON wire data. Every field in `Submission`
  (`request`, `credentials`, `group`, `stageIDs`) already is; adding a non-JSON field
  (Blob, Date, File) to a retry payload requires revisiting this contract.

### 4. Validation & Error Matrix

| Payload shape | `structuredClone` | JSON round-trip |
| --- | --- | --- |
| `models: []` (plain array from `.map`) | ok | ok |
| `models: [{ aliases: proxyArray }]` | throws `DataCloneError` | ok |
| `undefined` optional field | preserved as `undefined` | dropped (wire-equivalent) |

### 5. Good / Base / Bad Cases

- **Good**: `GroupCreatePanel.request()` returns reactive `aliases`; `begin()` freezes with
  `JSON.parse(JSON.stringify(payload))`, and one or more selected models create successfully.
- **Base**: zero models — the empty array was always cloneable, so this path never failed.
- **Bad**: `structuredClone(payload)` in `begin()`. The thrown error is swallowed by the
  panel's `try/catch`, which shows the generic "create failed" toast and **no HTTP request
  is sent** — the server log stays empty, which is the tell-tale sign.

### 6. Tests Required

No frontend test runner exists. Verify with:

- `pnpm run type-check` in `web/`.
- A Node one-liner: build the payload from a `ref` with a non-empty `aliases` array, clone
  it with the chosen expression, then mutate the original and assert the clone is
  unaffected.
- Manual: create a group with at least one selected model; after a target conflict, both
  "create anyway" and "append credentials" paths still submit.

### 7. Wrong vs Correct

#### Wrong

```ts
operation.value = { key: createOperationKey(), payload: structuredClone(payload) }
```

#### Correct

```ts
// 载荷本身就是 JSON wire 数据；Vue 响应式数组是 Proxy，structuredClone 会抛错。
operation.value = {
  key: createOperationKey(),
  payload: JSON.parse(JSON.stringify(payload)) as Submission,
}
```
