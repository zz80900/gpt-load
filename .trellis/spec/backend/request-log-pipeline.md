# Request Log Pipeline

> How a per-request field travels from the inbound request to the monitor page.

---

## Scenario: Adding a field to the request log

### 1. Scope / Trigger

The request log is produced by six layers. A field added to one layer but not to
the others either fails to compile, fails an exact-field-allowlist test, or —
worst case — makes the whole log API unusable for the frontend. Use this spec
whenever a field is added to, or removed from, the request log.

```
L1 dialect      internal/dialect/            parse the inbound request
L2 gateway      internal/gateway/            recorder collects the value
L3 telemetry    internal/telemetry/          RequestEvent crosses to the sink
L4 persistence  internal/storage + requestlog/  column, mapper, Record
L5 query/API    internal/requestlog/query.go, internal/control/
L6 frontend     web/src/app/resources/, web/src/features/monitor/
```

### 2. Signatures

```
DB      request_logs.anthropic_betas VARCHAR(1024) NOT NULL DEFAULT ''
Go      dialect.RequestMetadata.AnthropicBetas []string
        gateway.requestRecorder.setAnthropicBetas([]string)
        telemetry.RequestEvent.AnthropicBetas []string
        requestlog.Record.AnthropicBetas string
JSON    { "anthropic_betas": string }        // comma-separated, "" when undeclared
TS      RequestLogItemDto.anthropic_betas: string
```

### 3. Contracts

- **Sources**: the `Anthropic-Beta` request header (comma-separated, repeatable)
  and the top-level `betas` array of the request body. Both are unioned,
  trimmed, de-duplicated, sorted. Case is never normalised — the client's exact
  casing is the fact, and upstream matches beta identifiers exactly.
- **Encoding**: beta identifiers come from the HTTP token character set, which
  excludes commas, so joining with `,` is unambiguous and matches the header
  spelling.
- **Empty value**: `""` means "the client declared nothing". It does not mean
  "one million context is off" — the field records a declaration, never an
  upstream acceptance.
- **Collection is side-band**: a malformed body or an unparseable source drops
  that source only. `inspectAnthropicBetas` never returns an error and never
  panics; it sits on the inbound path.
- **Redaction**: this is the client's own declaration, the same class as
  `ClientModel`. It is **not** cleared by `sanitizeAccessKeyRequestLog` — it
  reveals no gateway topology. Do not add it to that list.
- **Frontend allowlist**: `web/src/app/resources/projector.ts` validates every
  response record against `itemFields` and rejects unknown keys with
  `invalidResponse()`. Adding a backend field without adding it to `itemFields`
  invalidates the **entire** log response, not just the new field. Backend and
  frontend must ship as one change.

### 4. Validation & Error Matrix

| Condition | Behaviour |
| --- | --- |
| Header absent, body has no `betas` | Empty slice → column `''` |
| `betas` is not an array, or body is not JSON | That source is dropped silently |
| Header value has blank items or padding | Items trimmed, blanks dropped |
| Same beta in header and body | Stored once |
| Joined value exceeds 1024 bytes | UTF-8-safe truncation, `...[truncated]` suffix |
| Invalid UTF-8 in a beta identifier | Replaced with U+FFFD before length checks |
| Non-Anthropic protocol | Field never filled, column `''` |
| Legacy row (pre-0015) | Column reads back as `''`, never NULL |

### 5. Good / Base / Bad Cases

- **Good**: `Anthropic-Beta: context-1m-2025-08-07` plus
  `{"betas":["prompt-caching-2024-07-31"]}` stores both, sorted, in one column.
- **Base**: an OpenAI-compatible request stores `''` and the monitor page shows
  no badge.
- **Bad**: the recorder keeps the caller's slice, the caller reuses it for the
  next request, and the log records betas the client never declared.

### 6. Tests Required

| Test | Assertion point |
| --- | --- |
| `internal/dialect/anthropic_betas_test.go` | Header source, body source, union with de-duplication, mixed-case header names, silent degradation, empty for non-Anthropic protocols |
| `internal/gateway/request_log_test.go` | The setter copies (mutating the caller's slice afterwards does not change the event); nil receiver is safe; undeclared emits an empty slice |
| `internal/telemetry/requestlog_test.go` | Reflection allowlist is an **exact** field list — a new field must be listed in declaration order |
| `internal/requestlog/mapper_test.go` | Empty input → `''`; over-long input truncated within the column limit and still valid UTF-8 |
| `internal/control/request_logs_test.go` | Field is present in the JSON response; it survives the access-key-scoped redaction path |
| `internal/storage/anthropic_betas_migration_test.go` | Fresh / upgrade / interrupted scenarios; legacy rows keep `''` |

### 7. Wrong vs Correct

#### Wrong

```go
// The caller keeps ownership of the slice and reuses it across requests.
func (recorder *requestRecorder) setAnthropicBetas(betas []string) {
	recorder.anthropicBetas = betas
}

// Per-attempt metadata; the declaration does not change between retries.
type Attempt struct {
	AnthropicBetas []string
}

// Cleared on the access-key path even though the client sent it itself.
func sanitizeAccessKeyRequestLog(record *requestlog.Record) {
	record.AnthropicBetas = ""
}
```

#### Correct

```go
// Copied at the boundary: the recorder reads it for the rest of its lifetime.
func (recorder *requestRecorder) setAnthropicBetas(betas []string) {
	if recorder == nil {
		return
	}
	recorder.anthropicBetas = append([]string(nil), betas...)
}
```

`AnthropicBetas` belongs to `RequestEvent`, not `Attempt`; it is a request-level
declaration. If it ever needs filtering, evaluate a join table instead of
`LIKE`-matching the comma-separated column.

