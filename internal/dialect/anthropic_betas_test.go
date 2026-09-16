package dialect

import (
	"net/http"
	"reflect"
	"testing"
)

func TestInspectAnthropicBetasCollectsDeclaredCapabilities(t *testing.T) {
	t.Parallel()

	requestBody := `{"model":"claude-sonnet-4-5","max_tokens":1024}`
	tests := []struct {
		name   string
		header http.Header
		body   string
		want   []string
	}{
		{
			name: "no declaration",
			body: requestBody,
			want: nil,
		},
		{
			name:   "header single value",
			header: http.Header{"Anthropic-Beta": {"context-1m-2025-08-07"}},
			body:   requestBody,
			want:   []string{"context-1m-2025-08-07"},
		},
		{
			name:   "header comma separated values are sorted",
			header: http.Header{"Anthropic-Beta": {"oauth-2025-04-20, context-1m-2025-08-07"}},
			body:   requestBody,
			want:   []string{"context-1m-2025-08-07", "oauth-2025-04-20"},
		},
		{
			name: "repeated header lines are merged",
			header: http.Header{
				"Anthropic-Beta": {"oauth-2025-04-20", "context-1m-2025-08-07"},
			},
			body: requestBody,
			want: []string{"context-1m-2025-08-07", "oauth-2025-04-20"},
		},
		{
			name:   "blank and empty entries are dropped",
			header: http.Header{"Anthropic-Beta": {"  , context-1m-2025-08-07 ,, "}},
			body:   requestBody,
			want:   []string{"context-1m-2025-08-07"},
		},
		{
			name: "body array is collected",
			body: `{"model":"claude-sonnet-4-5","betas":["context-1m-2025-08-07"]}`,
			want: []string{"context-1m-2025-08-07"},
		},
		{
			name:   "header and body are unioned and deduplicated",
			header: http.Header{"Anthropic-Beta": {"context-1m-2025-08-07"}},
			body:   `{"model":"claude-sonnet-4-5","betas":["context-1m-2025-08-07","oauth-2025-04-20"]}`,
			want:   []string{"context-1m-2025-08-07", "oauth-2025-04-20"},
		},
		{
			name:   "case is preserved rather than normalized",
			header: http.Header{"Anthropic-Beta": {"Context-1M-2025-08-07"}},
			body:   requestBody,
			want:   []string{"Context-1M-2025-08-07"},
		},
		{
			name:   "malformed body falls back to header",
			header: http.Header{"Anthropic-Beta": {"context-1m-2025-08-07"}},
			body:   `not json`,
			want:   []string{"context-1m-2025-08-07"},
		},
		{
			name:   "non array betas falls back to header",
			header: http.Header{"Anthropic-Beta": {"context-1m-2025-08-07"}},
			body:   `{"model":"claude-sonnet-4-5","betas":"context-1m-2025-08-07"}`,
			want:   []string{"context-1m-2025-08-07"},
		},
		{
			name:   "empty body falls back to header",
			header: http.Header{"Anthropic-Beta": {"context-1m-2025-08-07"}},
			body:   "",
			want:   []string{"context-1m-2025-08-07"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := inspectAnthropicBetas(test.header, []byte(test.body))
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("betas = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestInspectAnthropicBetasReadsHeadersSetWithMixedCaseNames(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Set("anthropic-beta", "context-1m-2025-08-07")

	got := inspectAnthropicBetas(header, []byte(`{"model":"claude-sonnet-4-5"}`))
	want := []string{"context-1m-2025-08-07"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("betas = %#v, want %#v", got, want)
	}
}

func TestInspectRequestCapturesAnthropicBetas(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Set("Anthropic-Beta", "oauth-2025-04-20, context-1m-2025-08-07")

	metadata, err := NewAnthropic().InspectRequest(&ParsedRequest{
		Method: http.MethodPost,
		Path:   "/v1/messages",
		Header: header,
		Body:   []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"betas":["oauth-2025-04-20"]}`),
	})
	if err != nil {
		t.Fatalf("inspect request: %v", err)
	}

	want := []string{"context-1m-2025-08-07", "oauth-2025-04-20"}
	if !reflect.DeepEqual(metadata.AnthropicBetas, want) {
		t.Fatalf("betas = %#v, want %#v", metadata.AnthropicBetas, want)
	}
}

func TestInspectRequestCapturesAnthropicBetasForCountTokens(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Set("Anthropic-Beta", "context-1m-2025-08-07")

	metadata, err := NewAnthropic().InspectRequest(&ParsedRequest{
		Method: http.MethodPost,
		Path:   "/v1/messages/count_tokens",
		Header: header,
		Body:   []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024}`),
	})
	if err != nil {
		t.Fatalf("inspect request: %v", err)
	}

	want := []string{"context-1m-2025-08-07"}
	if !reflect.DeepEqual(metadata.AnthropicBetas, want) {
		t.Fatalf("betas = %#v, want %#v", metadata.AnthropicBetas, want)
	}
}

func TestInspectRequestLeavesBetasEmptyForOtherProtocols(t *testing.T) {
	t.Parallel()

	header := http.Header{}
	header.Set("Anthropic-Beta", "context-1m-2025-08-07")

	metadata, err := NewOpenAI().InspectRequest(&ParsedRequest{
		Method: http.MethodPost,
		Path:   "/v1/chat/completions",
		Header: header,
		Body:   []byte(`{"model":"gpt-5"}`),
	})
	if err != nil {
		t.Fatalf("inspect request: %v", err)
	}

	if len(metadata.AnthropicBetas) != 0 {
		t.Fatalf("betas = %#v, want empty", metadata.AnthropicBetas)
	}
}
