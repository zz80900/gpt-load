package state

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"gpt-load/internal/accessquota"
	"gpt-load/internal/catalog"
	"gpt-load/internal/channel"
	"gpt-load/internal/execution"
	"gpt-load/internal/platform/config"
	"gpt-load/internal/protocol"
)

func TestCompileIndexesExternalModelsAndPreservesUpstreamIDs(t *testing.T) {
	t.Parallel()

	input := CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, Name: "one", ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{
					{ID: "provider-a", Aliases: []string{"public"}},
					{ID: "provider-a", Aliases: []string{"secondary"}},
					{ID: "plain"},
				},
				Enabled: true,
			},
			{ConnectionType: "api_key", ID: 2, Name: "two", ChannelID: channel.OpenAICompatible,
				Params:  json.RawMessage(`{"base_url":"https://proxy.example/v1"}`),
				Models:  []ModelConfig{{ID: "provider-b", Aliases: []string{"public"}}},
				Enabled: true,
			},
		},
	}

	snapshot, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	index := snapshot.ExecutionCandidates[protocol.OpenAICompletions][execution.OperationChatCompletion]
	public := index["public"]
	if len(public) != 2 || public[0].UpstreamModelID != "provider-a" || public[1].UpstreamModelID != "provider-b" {
		t.Fatalf("public targets = %#v", public)
	}
	if got := index["secondary"]; len(got) != 1 || got[0].UpstreamModelID != "provider-a" {
		t.Fatalf("secondary targets = %#v", got)
	}
	if got := index["plain"]; len(got) != 1 || got[0].UpstreamModelID != "plain" {
		t.Fatalf("plain targets = %#v", got)
	}
	// 上游 ID 与别名并列注册：配了别名之后原 ID 仍然可路由。
	if got := index["provider-a"]; len(got) != 1 || got[0].UpstreamModelID != "provider-a" {
		t.Fatalf("upstream id targets = %#v", got)
	}
}

func TestRoutableModelNamesExpandsContextSuffixBase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model ModelConfig
		want  []string
	}{
		{
			name:  "without suffix matches external names",
			model: ModelConfig{ID: "provider-a", Aliases: []string{"public", "secondary"}},
			want:  []string{"provider-a", "public", "secondary"},
		},
		{
			name:  "alias suffix derives base after the alias",
			model: ModelConfig{ID: "deepseek-flash", Aliases: []string{"claude-deepseek-flash[1M]"}},
			want:  []string{"deepseek-flash", "claude-deepseek-flash[1M]", "claude-deepseek-flash"},
		},
		{
			name:  "lower suffix derives base as well",
			model: ModelConfig{ID: "deepseek-flash", Aliases: []string{"claude-deepseek-flash[1m]"}},
			want:  []string{"deepseek-flash", "claude-deepseek-flash[1m]", "claude-deepseek-flash"},
		},
		{
			name:  "suffix only alias derives nothing",
			model: ModelConfig{ID: "deepseek-flash", Aliases: []string{"[1M]"}},
			want:  []string{"deepseek-flash", "[1M]"},
		},
		{
			name:  "id suffix derives base",
			model: ModelConfig{ID: "deepseek-flash[1M]"},
			want:  []string{"deepseek-flash[1M]", "deepseek-flash"},
		},
		{
			name:  "derived name duplicating the id is registered once",
			model: ModelConfig{ID: "xxxx", Aliases: []string{"xxxx[1M]"}},
			want:  []string{"xxxx", "xxxx[1M]"},
		},
		{
			name:  "derived name duplicating a later alias is registered once",
			model: ModelConfig{ID: "xxxx[1M]", Aliases: []string{"xxxx"}},
			want:  []string{"xxxx[1M]", "xxxx"},
		},
		{
			name:  "wildcard alias is kept verbatim",
			model: ModelConfig{ID: "deepseek-flash", Aliases: []string{"claude-*"}},
			want:  []string{"deepseek-flash", "claude-*"},
		},
		{
			name:  "wildcard alias derives its suffix-stripped pattern",
			model: ModelConfig{ID: "deepseek-flash", Aliases: []string{"claude-*[1m]"}},
			want:  []string{"deepseek-flash", "claude-*[1m]", "claude-*"},
		},
		{
			name:  "bare wildcard is kept verbatim",
			model: ModelConfig{ID: "deepseek-flash", Aliases: []string{"*"}},
			want:  []string{"deepseek-flash", "*"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := RoutableModelNames(test.model)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("RoutableModelNames(%#v) = %#v, want %#v", test.model, got, test.want)
			}
		})
	}
}

// 无后缀模型是存量配置的绝大多数，两个名称集合必须逐项相等：任何差异都会改变
// 路由索引、模型页与冲突检测的既有行为。
func TestRoutableModelNamesMatchesExternalNamesWithoutSuffix(t *testing.T) {
	t.Parallel()

	models := []ModelConfig{
		{ID: "provider-a", Aliases: []string{"public", "secondary"}},
		{ID: "plain"},
		{ID: "  padded  ", Aliases: []string{"  public  ", "", "public", "plain"}},
		{ID: "bracketed", Aliases: []string{"bracketed[2M]", "xx[128K]yy"}},
		// 不带上下文后缀的通配符别名同样是恒等项：模式不会让两个集合产生差异。
		{ID: "deepseek-flash", Aliases: []string{"claude-*", "gpt-*"}},
		{},
	}
	for _, model := range models {
		want := ExternalModelNames(model)
		if got := RoutableModelNames(model); !reflect.DeepEqual(got, want) {
			t.Fatalf("RoutableModelNames(%#v) = %#v, want ExternalModelNames %#v", model, got, want)
		}
	}
}

func TestCompileIndexesContextSuffixAliasBase(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, Name: "one", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "deepseek-flash", Aliases: []string{"claude-deepseek-flash[1M]"}}}, Enabled: true},
			// 同一个基名的另一条目的一条别名，验证派生名不会串到别的上游。
			{ConnectionType: "api_key", ID: 2, Name: "two", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "qwen-flash", Aliases: []string{"claude-qwen-flash[1m]"}}}, Enabled: true},
			// 基名为空的别名不产生派生名：索引里不能出现空键。
			{ConnectionType: "api_key", ID: 3, Name: "three", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "suffix-only", Aliases: []string{"[1M]"}}}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	for indexName, index := range map[string]ExecutionCandidateIndex{
		"candidates":    snapshot.ExecutionCandidates,
		"route catalog": snapshot.ExecutionRouteCatalog,
	} {
		byModel := index[protocol.Anthropic][execution.OperationChatCompletion]
		for name, wantUpstream := range map[string]string{
			"claude-deepseek-flash[1M]": "deepseek-flash",
			"claude-deepseek-flash":     "deepseek-flash",
			"claude-qwen-flash[1m]":     "qwen-flash",
			"claude-qwen-flash":         "qwen-flash",
			"deepseek-flash":            "deepseek-flash",
			"[1M]":                      "suffix-only",
		} {
			targets := byModel[name]
			if len(targets) != 1 || targets[0].UpstreamModelID != wantUpstream {
				t.Fatalf("%s targets for %q = %#v, want upstream %q", indexName, name, targets, wantUpstream)
			}
		}
		if targets, exists := byModel[NoModelRouteKey]; exists {
			t.Fatalf("%s registered an empty model key: %#v", indexName, targets)
		}
	}
}

// 模式必须进独立的模式索引，不得成为精确索引的键：精确索引的键会被 /v1/models 直接
// 枚举成模型列表，把 claude-* 放进去等于向客户端广播一个不存在的模型名。
func TestCompileSeparatesPatternsFromExactKeys(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, Name: "one", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{
					{ID: "deepseek-flash", Aliases: []string{"claude-*[1m]", "exact-alias"}},
				}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	for indexName, index := range map[string]ExecutionCandidateIndex{
		"candidates":    snapshot.ExecutionCandidates,
		"route catalog": snapshot.ExecutionRouteCatalog,
	} {
		byModel := index[protocol.Anthropic][execution.OperationChatCompletion]
		for _, name := range []string{"claude-*[1m]", "claude-*"} {
			if targets, exists := byModel[name]; exists {
				t.Fatalf("%s registered pattern %q as an exact key: %#v", indexName, name, targets)
			}
		}
		for name, wantUpstream := range map[string]string{
			"deepseek-flash": "deepseek-flash",
			"exact-alias":    "deepseek-flash",
		} {
			targets := byModel[name]
			if len(targets) != 1 || targets[0].UpstreamModelID != wantUpstream {
				t.Fatalf("%s exact targets for %q = %#v, want upstream %q", indexName, name, targets, wantUpstream)
			}
		}
	}

	for indexName, index := range map[string]ModelPatternIndex{
		"candidates":    snapshot.ExecutionPatterns,
		"route catalog": snapshot.ExecutionRoutePatterns,
	} {
		patterns := index[protocol.Anthropic][execution.OperationChatCompletion]
		if len(patterns) != 2 {
			t.Fatalf("%s patterns = %#v, want 2 entries", indexName, patterns)
		}
		for _, entry := range patterns {
			if len(entry.Targets) != 1 || entry.Targets[0].UpstreamModelID != "deepseek-flash" {
				t.Fatalf("%s pattern %q targets = %#v", indexName, entry.Pattern, entry.Targets)
			}
		}
		// claude-*[1m] 与 claude-* 的字面前缀长度分别是 8 与 7，因此前者更具体、排在前面。
		if patterns[0].Pattern != "claude-*[1m]" || patterns[1].Pattern != "claude-*" {
			t.Fatalf("%s pattern order = %#v, want [claude-*[1m] claude-*]", indexName, patterns)
		}
	}
}

// 模式不与精确名构成冲突，也不与另一个不同的重叠模式构成冲突：冲突判定按精确字符串
// 认领，而这三者字符串互不相等。请求最终走哪条由「精确优先 + 模式特异性」决定，
// 因此共存是安全的，把它判成冲突会否掉本特性存在的理由。
func TestCompileAcceptsPatternsAlongsideExactNamesAndOtherPatterns(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, Name: "one", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{
					{ID: "exact-owner", Aliases: []string{"claude-sonnet-5"}},
					{ID: "broad", Aliases: []string{"claude-*"}},
					{ID: "specific", Aliases: []string{"claude-sonnet-*"}},
				}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	byModel := snapshot.ExecutionCandidates[protocol.Anthropic][execution.OperationChatCompletion]
	if targets := byModel["claude-sonnet-5"]; len(targets) != 1 || targets[0].UpstreamModelID != "exact-owner" {
		t.Fatalf("exact targets = %#v, want upstream exact-owner", targets)
	}
	for _, name := range []string{"claude-*", "claude-sonnet-*"} {
		if targets, exists := byModel[name]; exists {
			t.Fatalf("pattern %q leaked into the exact index: %#v", name, targets)
		}
	}
	patterns := snapshot.ExecutionPatterns[protocol.Anthropic][execution.OperationChatCompletion]
	if len(patterns) != 2 {
		t.Fatalf("patterns = %#v, want two coexisting patterns", patterns)
	}
}

// 同一个模式串跨分组出现时聚合为一条：与精确名称跨分组聚合的行为一致，否则同一模式
// 会被重复认领，放大该目标的候选权重。
func TestCompileAggregatesPatternsAcrossGroups(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, Name: "one", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "upstream-a", Aliases: []string{"claude-*"}}}, Enabled: true},
			{ConnectionType: "api_key", ID: 2, Name: "two", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "upstream-b", Aliases: []string{"claude-*"}}}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	patterns := snapshot.ExecutionPatterns[protocol.Anthropic][execution.OperationChatCompletion]
	if len(patterns) != 1 {
		t.Fatalf("patterns = %#v, want a single aggregated entry", patterns)
	}
	targets := patterns[0].Targets
	if len(targets) != 2 || targets[0].UpstreamModelID != "upstream-a" || targets[1].UpstreamModelID != "upstream-b" {
		t.Fatalf("aggregated targets = %#v, want upstream-a then upstream-b", targets)
	}
}

// 裁决顺序必须只由配置内容决定：打乱分组顺序后模式序列必须逐项相同。
func TestCompileOrdersPatternsBySpecificityRegardlessOfGroupOrder(t *testing.T) {
	t.Parallel()

	compile := func(groups []GroupConfig) []ModelPattern {
		t.Helper()
		snapshot, err := Compile(CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: groups})
		if err != nil {
			t.Fatalf("Compile() error = %v", err)
		}
		return snapshot.ExecutionPatterns[protocol.Anthropic][execution.OperationChatCompletion]
	}

	broad := GroupConfig{ConnectionType: "api_key", ID: 1, Name: "broad", ChannelID: channel.Anthropic,
		Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "upstream-broad", Aliases: []string{"claude-*"}}}, Enabled: true}
	specific := GroupConfig{ConnectionType: "api_key", ID: 2, Name: "specific", ChannelID: channel.Anthropic,
		Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "upstream-specific", Aliases: []string{"claude-sonnet-*"}}}, Enabled: true}

	forward := compile([]GroupConfig{broad, specific})
	reversed := compile([]GroupConfig{specific, broad})
	if !reflect.DeepEqual(forward, reversed) {
		t.Fatalf("pattern order depends on group order:\nforward  = %#v\nreversed = %#v", forward, reversed)
	}
	if len(forward) != 2 || forward[0].Pattern != "claude-sonnet-*" || forward[1].Pattern != "claude-*" {
		t.Fatalf("pattern order = %#v, want [claude-sonnet-* claude-*]", forward)
	}
}

// Manager.Matches 对整个快照做 reflect.DeepEqual：模式索引的构建必须确定，否则每次
// reconcile 都会判定为「配置已变更」，表现为随机的 503 configuration_changed。
func TestCompilePatternIndexIsDeterministic(t *testing.T) {
	t.Parallel()

	input := CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, Name: "one", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "upstream-a", Aliases: []string{"claude-*", "gpt-*"}}}, Enabled: true},
			{ConnectionType: "api_key", ID: 2, Name: "two", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "upstream-b", Aliases: []string{"claude-sonnet-*"}}}, Enabled: true},
			{ConnectionType: "api_key", ID: 3, Name: "three", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "upstream-c", Aliases: []string{"claude-*-5", "*-sonnet"}}}, Enabled: true},
		},
	}
	first, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	for attempt := range 8 {
		next, err := Compile(input)
		if err != nil {
			t.Fatalf("Compile() attempt %d error = %v", attempt, err)
		}
		if !reflect.DeepEqual(first.ExecutionPatterns, next.ExecutionPatterns) {
			t.Fatalf("attempt %d produced a different candidate pattern index", attempt)
		}
		if !reflect.DeepEqual(first.ExecutionRoutePatterns, next.ExecutionRoutePatterns) {
			t.Fatalf("attempt %d produced a different catalog pattern index", attempt)
		}
	}
}

// 巡检目录对禁用分组也注册（Compile 在 Enabled 判断之前调用），数据面候选只注册启用
// 分组。两个模式索引必须保持同样的分野，否则路由巡检查不出数据面查得到的路由。
func TestCompilePatternsFollowEnabledSplitBetweenIndexes(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, Name: "disabled", ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "upstream-disabled", Aliases: []string{"claude-*"}}}, Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if patterns := snapshot.ExecutionPatterns[protocol.Anthropic][execution.OperationChatCompletion]; len(patterns) != 0 {
		t.Fatalf("candidate patterns = %#v, want none for a disabled group", patterns)
	}
	catalog := snapshot.ExecutionRoutePatterns[protocol.Anthropic][execution.OperationChatCompletion]
	if len(catalog) != 1 || catalog[0].Pattern != "claude-*" {
		t.Fatalf("catalog patterns = %#v, want the disabled group's pattern", catalog)
	}
	if targets := snapshot.ExecutionCandidates[protocol.Anthropic][execution.OperationChatCompletion]; len(targets) != 0 {
		t.Fatalf("candidate exact keys = %#v, want none for a disabled group", targets)
	}
}

// 无通配符的配置必须逐项不变：模式索引为空，两个具体名称集合与既有集合逐项相等。
func TestConcreteModelNamesDropsOnlyPatterns(t *testing.T) {
	t.Parallel()

	models := []ModelConfig{
		{ID: "provider-a", Aliases: []string{"public", "secondary"}},
		{ID: "deepseek-flash", Aliases: []string{"claude-*"}},
		{ID: "deepseek-flash", Aliases: []string{"claude-*[1m]"}},
		{ID: "plain"},
		{},
	}
	for _, model := range models {
		concrete := ConcreteModelNames(model)
		for _, name := range concrete {
			if strings.Contains(name, "*") {
				t.Fatalf("ConcreteModelNames(%#v) leaked pattern %q", model, name)
			}
		}
		concreteRoutable := ConcreteRoutableModelNames(model)
		for _, name := range concreteRoutable {
			if strings.Contains(name, "*") {
				t.Fatalf("ConcreteRoutableModelNames(%#v) leaked pattern %q", model, name)
			}
		}
		if len(concrete) > len(ExternalModelNames(model)) ||
			len(concreteRoutable) > len(RoutableModelNames(model)) {
			t.Fatalf("filter produced more names than its source for %#v", model)
		}
	}

	// 无通配符时两个过滤器是恒等函数。
	plain := ModelConfig{ID: "provider-a", Aliases: []string{"claude-opus-5", "public[1m]"}}
	if got, want := ConcreteModelNames(plain), ExternalModelNames(plain); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConcreteModelNames(%#v) = %#v, want %#v", plain, got, want)
	}
	if got, want := ConcreteRoutableModelNames(plain), RoutableModelNames(plain); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConcreteRoutableModelNames(%#v) = %#v, want %#v", plain, got, want)
	}

	// 有通配符时只丢掉模式项，其余逐项保留且顺序不变。
	wildcard := ModelConfig{ID: "deepseek-flash", Aliases: []string{"claude-*[1m]", "public"}}
	if got, want := ConcreteModelNames(wildcard), []string{"deepseek-flash", "public"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ConcreteModelNames(%#v) = %#v, want %#v", wildcard, got, want)
	}
	if got, want := ConcreteRoutableModelNames(wildcard), []string{"deepseek-flash", "public"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ConcreteRoutableModelNames(%#v) = %#v, want %#v", wildcard, got, want)
	}
}

func TestCompileIndexesOpenAIImagesOperationsForAllConfiguredModels(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "official-image", Aliases: []string{"public"}}}, Enabled: true},
			{ConnectionType: "api_key", ID: 2, ChannelID: channel.OpenAICompatible,
				Params: json.RawMessage(`{"base_url":"https://proxy.example/api/v4"}`),
				Models: []ModelConfig{{ID: "compatible-image", Aliases: []string{"public"}}}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	for _, operation := range []execution.Operation{execution.OperationImagesGenerate, execution.OperationImagesEdit} {
		got := snapshot.ExecutionCandidates[protocol.OpenAIImages][operation]["public"]
		if len(got) != 2 || got[0].GroupID != 1 || got[1].GroupID != 2 ||
			got[0].Mode != channel.RouteNative || got[1].Mode != channel.RouteNative {
			t.Errorf("Images candidates for %q = %#v", operation, got)
		}
	}
}

func TestCompileIndexesOpenAIEmbeddingsForAllConfiguredModels(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "text-embedding-3-small", Aliases: []string{"public"}}}, Enabled: true},
			{ConnectionType: "api_key", ID: 2, ChannelID: channel.OpenAICompatible,
				Params: json.RawMessage(`{"base_url":"https://proxy.example/api/v4"}`),
				Models: []ModelConfig{{ID: "Qwen/Qwen3-Embedding-8B", Aliases: []string{"public"}}}, Enabled: true},
			{ConnectionType: "api_key", ID: 3, ChannelID: channel.OpenRouter, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "openai/text-embedding-3-small", Aliases: []string{"public"}}}, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	got := snapshot.ExecutionCandidates[protocol.OpenAIEmbeddings][execution.OperationEmbeddingsCreate]["public"]
	if len(got) != 3 || got[0].GroupID != 1 || got[1].GroupID != 2 || got[2].GroupID != 3 {
		t.Fatalf("Embeddings candidates = %#v", got)
	}
	for _, target := range got {
		if target.Mode != channel.RouteNative {
			t.Fatalf("Embeddings target = %#v, want native", target)
		}
	}
}

func TestCompileSubscriptionPublishesOnlyVerifiedCodexOperations(t *testing.T) {
	t.Parallel()
	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{{
			ID: 1, Name: "subscription", ChannelID: channel.Codex, ConnectionType: "subscription",
			Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "gpt-5", Aliases: []string{"public"}}}, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if got := snapshot.ExecutionCandidates[protocol.OpenAICompletions][execution.OperationChatCompletion]["public"]; len(got) != 1 {
		t.Fatalf("chat targets = %#v", got)
	}
	if got := snapshot.ExecutionCandidates[protocol.OpenAIResponses][execution.OperationResponsesCreate]["public"]; len(got) != 1 {
		t.Fatalf("responses create targets = %#v", got)
	}
	if got := snapshot.ExecutionCandidates[protocol.OpenAIResponses][execution.OperationResponsesInputTokens]["public"]; len(got) != 1 {
		t.Fatalf("responses input token targets = %#v", got)
	}
	if got := snapshot.ExecutionCandidates[protocol.Anthropic][execution.OperationCountTokens]["public"]; len(got) != 1 {
		t.Fatalf("Anthropic count token targets = %#v", got)
	}
	if got := snapshot.ExecutionCandidates[protocol.Gemini][execution.OperationCountTokens]["public"]; len(got) != 1 {
		t.Fatalf("Gemini count token targets = %#v", got)
	}
	if got := snapshot.ExecutionCandidates[protocol.OpenAIEmbeddings]; len(got) != 0 {
		t.Fatalf("subscription Embeddings targets = %#v, want none", got)
	}
	for _, operation := range []execution.Operation{
		execution.OperationResponsesRetrieve, execution.OperationResponsesDelete,
		execution.OperationResponsesCancel, execution.OperationResponsesCompact,
		execution.OperationResponsesInputItems,
		execution.OperationResponsesPassthrough,
	} {
		if got := snapshot.ExecutionCandidates[protocol.OpenAIResponses][operation]; len(got) != 0 {
			t.Fatalf("unsupported subscription operation %q was published: %#v", operation, got)
		}
	}
}

func TestCompileBuildsManagementCatalogsWithoutChangingActiveIndexes(t *testing.T) {
	t.Parallel()

	disabledWeight := 20
	input := CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{
			{ConnectionType: "api_key", ID: 2, Name: "disabled", ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "provider-disabled", Aliases: []string{"public"}}}, WeightManual: &disabledWeight,
			},
			{ConnectionType: "api_key", ID: 1, Name: "active", ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
				Models: []ModelConfig{{ID: "provider-active", Aliases: []string{"public"}}}, Enabled: true,
			},
		},
		AccessKeys: []AccessKeyConfig{
			{ID: 11, Name: "active-client", KeyHash: "active-hash", Status: AccessKeyStatusActive, Filters: FilterSet{Groups: map[uint]struct{}{1: {}}}, RPMLimit: 10},
			{ID: 12, Name: "disabled-client", KeyHash: "disabled-hash", Status: AccessKeyStatusDisabled, Filters: FilterSet{Models: map[string]struct{}{"public": {}}}, RPMLimit: 20},
		},
	}

	snapshot, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(snapshot.Groups) != 1 || snapshot.Groups[1].Name != "active" {
		t.Fatalf("active Groups = %#v", snapshot.Groups)
	}
	active := snapshot.ExecutionCandidates[protocol.OpenAICompletions][execution.OperationChatCompletion]["public"]
	if len(active) != 1 || active[0].GroupID != 1 {
		t.Fatalf("active candidates = %#v", active)
	}
	routes := snapshot.ExecutionRouteCatalog[protocol.OpenAICompletions][execution.OperationChatCompletion]["public"]
	if len(routes) != 2 || routes[0].GroupID != 1 || routes[1].GroupID != 2 {
		t.Fatalf("route catalog = %#v", routes)
	}
	if got := snapshot.GroupCatalog[2]; got.Enabled || got.WeightManual == nil || *got.WeightManual != 20 {
		t.Fatalf("disabled group catalog = %#v", got)
	}
	if _, ok := snapshot.AccessKeysByHash["disabled-hash"]; ok {
		t.Fatal("disabled access key entered active hash index")
	}
	if got := snapshot.AccessKeysByID[12]; got.Status != AccessKeyStatusDisabled || got.RPMLimit != 20 {
		t.Fatalf("disabled access key catalog = %#v", got)
	}
}

func TestCompileCarriesSettingsAndValidationModel(t *testing.T) {
	t.Parallel()

	snapshot, err := Compile(CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		SystemSettings:  config.Settings{"first_byte_timeout": json.Number("20")},
		Groups: []GroupConfig{{ConnectionType: "api_key", ID: 1, Name: "one", ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
			ValidationModel: "  probe-model  ",
			Models:          []ModelConfig{{ID: "real-model", Aliases: []string{"public-model"}}},
			Settings:        config.Settings{"request_timeout": json.Number("30")}, Enabled: true,
		}},
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	view := snapshot.Groups[1]
	if view.ValidationModel != "probe-model" || view.Timeouts.FirstByte.Seconds() != 20 || view.Timeouts.Request.Seconds() != 30 {
		t.Fatalf("group runtime view = %#v", view)
	}
}

func TestCompileOwnsInputData(t *testing.T) {
	t.Parallel()

	weight := 25
	expiresAtMS := int64(1_900_000_000_000)
	filters := FilterSet{
		Groups:    map[uint]struct{}{1: {}},
		Protocols: map[protocol.Protocol]struct{}{protocol.OpenAICompletions: {}},
		Models:    map[string]struct{}{"public": {}},
	}
	input := CompileInput{
		ChannelRegistry: channel.NewRegistry(),
		Groups: []GroupConfig{{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`),
			Models: []ModelConfig{{ID: "upstream", Aliases: []string{"public"}}}, WeightManual: &weight, Enabled: true,
		}},
		AccessKeys: []AccessKeyConfig{{
			ID: 1, KeyHash: "hash", Status: AccessKeyStatusActive, Filters: filters,
			ExpiresAtMS: &expiresAtMS,
			AllowedPeerCIDRs: []netip.Prefix{
				netip.MustParsePrefix("192.0.2.0/24"),
			},
			CostLimitRules: []accessquota.Rule{{
				ID: 7, Revision: 1, Kind: accessquota.KindTotal, LimitNanoUSD: 100,
			}},
		}},
		ClientModelOverrides: map[string]catalog.ClientModelOverrides{"public": {DisplayName: stringPointer("Public")}},
	}
	snapshot, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	input.Groups[0].Models[0] = ModelConfig{ID: "changed"}
	weight = 99
	filters.Groups[2] = struct{}{}
	filters.Protocols[protocol.Gemini] = struct{}{}
	filters.Models["changed"] = struct{}{}
	expiresAtMS = 1
	input.AccessKeys[0].AllowedPeerCIDRs[0] = netip.MustParsePrefix("198.51.100.0/24")
	input.AccessKeys[0].CostLimitRules[0].LimitNanoUSD = 1
	displayName := "Changed"
	input.ClientModelOverrides["public"] = catalog.ClientModelOverrides{DisplayName: &displayName}

	view := snapshot.Groups[1]
	if !reflect.DeepEqual(view.Models, []ModelConfig{{ID: "upstream", Aliases: []string{"public"}}}) || view.WeightManual == nil || *view.WeightManual != 25 {
		t.Fatalf("group view changed with input = %#v", view)
	}
	gotFilters := snapshot.AccessKeysByID[1].Filters
	if _, ok := gotFilters.Groups[2]; ok {
		t.Fatal("group filter retained caller mutation")
	}
	if _, ok := gotFilters.Protocols[protocol.Gemini]; ok {
		t.Fatal("protocol filter retained caller mutation")
	}
	if _, ok := gotFilters.Models["changed"]; ok {
		t.Fatal("model filter retained caller mutation")
	}
	if got := snapshot.AccessKeysByID[1].CostLimitRules; len(got) != 1 || got[0].LimitNanoUSD != 100 {
		t.Fatalf("cost limit rules changed with input = %#v", got)
	}
	accessKey := snapshot.AccessKeysByID[1]
	if accessKey.ExpiresAtMS == nil || *accessKey.ExpiresAtMS != 1_900_000_000_000 {
		t.Fatalf("access key expiry changed with input = %#v", accessKey.ExpiresAtMS)
	}
	if !reflect.DeepEqual(accessKey.AllowedPeerCIDRs, []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}) {
		t.Fatalf("access key CIDRs changed with input = %#v", accessKey.AllowedPeerCIDRs)
	}
	if profile := snapshot.ClientModelOverrides["public"]; profile.DisplayName == nil || *profile.DisplayName != "Public" {
		t.Fatalf("client model override changed with input = %#v", profile)
	}
}

func TestCompileValidatesClientModelOverrides(t *testing.T) {
	t.Parallel()

	name := "Public"
	input := CompileInput{ClientModelOverrides: map[string]catalog.ClientModelOverrides{
		"public": {DisplayName: &name},
	}}
	snapshot, err := Compile(input)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if got := snapshot.ClientModelOverrides["public"].DisplayName; got == nil || *got != "Public" {
		t.Fatalf("compiled overrides = %#v", snapshot.ClientModelOverrides)
	}
	for model, overrides := range map[string]catalog.ClientModelOverrides{
		"":        {DisplayName: &name},
		" public": {DisplayName: &name},
		"public":  {},
	} {
		input.ClientModelOverrides = map[string]catalog.ClientModelOverrides{model: overrides}
		if _, err := Compile(input); err == nil {
			t.Fatalf("Compile() accepted invalid client model override %q: %#v", model, overrides)
		}
	}
}

func TestCompileRejectsInvalidCoreConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   CompileInput
		wantErr string
	}{
		{
			name:    "duplicate external model",
			input:   CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []GroupConfig{{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "a"}, {ID: "b", Aliases: []string{"a"}}}, Enabled: true}}},
			wantErr: "duplicate external model",
		},
		{
			// 派生名（后缀基名）与他人显式名撞名：用户配置里没有 "xxxx" 这个重复项，
			// 报错必须指出它由哪条别名派生，否则无从定位。
			name:    "derived suffix name conflicts with explicit name",
			input:   CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []GroupConfig{{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "xxxx"}, {ID: "provider", Aliases: []string{"xxxx[1M]"}}}, Enabled: true}}},
			wantErr: `duplicate routed model "xxxx": model "provider" derives it from "xxxx[1M]"`,
		},
		{
			// 存量数据的另一种排列：带后缀别名先被认领，后一个条目显式写了基名。
			name:    "explicit name conflicts with derived suffix name",
			input:   CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []GroupConfig{{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "provider", Aliases: []string{"xxxx[1m]"}}, {ID: "xxxx"}}, Enabled: true}}},
			wantErr: `duplicate external model "xxxx": model "provider" already claims it by stripping the context suffix from "xxxx[1m]"`,
		},
		{
			// 同一个通配符模式被两个不同上游认领：路由结果会由候选排序而非配置决定，
			// 必须拒绝。用户两个条目写的是同一个字符串，报错直接给出该字符串。
			name:    "duplicate pattern across upstreams",
			input:   CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []GroupConfig{{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "provider-a", Aliases: []string{"claude-*"}}, {ID: "provider-b", Aliases: []string{"claude-*"}}}, Enabled: true}}},
			wantErr: `duplicate external model "claude-*"`,
		},
		{
			// 撞名的一方是后缀派生出来的模式：用户从没写过 "claude-*"，报错必须指出它
			// 由哪条别名派生，否则无从定位。
			name:    "derived pattern conflicts with an explicit pattern",
			input:   CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []GroupConfig{{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`), Models: []ModelConfig{{ID: "provider-a", Aliases: []string{"claude-*"}}, {ID: "provider-b", Aliases: []string{"claude-*[1m]"}}}, Enabled: true}}},
			wantErr: `duplicate routed model "claude-*": model "provider-b" derives it from "claude-*[1m]"`,
		},
		{
			name: "duplicate group id",
			input: CompileInput{ChannelRegistry: channel.NewRegistry(), Groups: []GroupConfig{
				{ConnectionType: "api_key", ID: 1, ChannelID: channel.OpenAI, Params: json.RawMessage(`{}`)},
				{ConnectionType: "api_key", ID: 1, ChannelID: channel.Anthropic, Params: json.RawMessage(`{}`)},
			}},
			wantErr: "duplicate group id",
		},
		{
			name: "duplicate access key hash",
			input: CompileInput{AccessKeys: []AccessKeyConfig{
				{ID: 1, KeyHash: "same", Status: AccessKeyStatusActive},
				{ID: 2, KeyHash: "same", Status: AccessKeyStatusActive},
			}},
			wantErr: "duplicate access key hash",
		},
		{
			name: "invalid cost limit rules",
			input: CompileInput{AccessKeys: []AccessKeyConfig{{
				ID: 1, KeyHash: "hash", Status: AccessKeyStatusActive,
				CostLimitRules: []accessquota.Rule{
					{ID: 1, Revision: 1, Kind: accessquota.KindTotal, LimitNanoUSD: 100},
					{ID: 2, Revision: 1, Kind: accessquota.KindTotal, LimitNanoUSD: 200},
				},
			}}},
			wantErr: "cost limit",
		},
		{
			name: "negative access key expiry",
			input: CompileInput{AccessKeys: []AccessKeyConfig{{
				ID: 1, KeyHash: "hash", Status: AccessKeyStatusActive,
				ExpiresAtMS: int64Pointer(-1),
			}}},
			wantErr: "expiry",
		},
		{
			name: "unsafe access key expiry",
			input: CompileInput{AccessKeys: []AccessKeyConfig{{
				ID: 1, KeyHash: "hash", Status: AccessKeyStatusActive,
				ExpiresAtMS: int64Pointer(9_007_199_254_740_992),
			}}},
			wantErr: "expiry",
		},
		{
			name: "invalid allowed peer cidr",
			input: CompileInput{AccessKeys: []AccessKeyConfig{{
				ID: 1, KeyHash: "hash", Status: AccessKeyStatusActive,
				AllowedPeerCIDRs: []netip.Prefix{{}},
			}}},
			wantErr: "CIDR",
		},
		{
			name: "non canonical allowed peer cidr",
			input: CompileInput{AccessKeys: []AccessKeyConfig{{
				ID: 1, KeyHash: "hash", Status: AccessKeyStatusActive,
				AllowedPeerCIDRs: []netip.Prefix{netip.MustParsePrefix("192.0.2.99/24")},
			}}},
			wantErr: "CIDR",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile(test.input)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Compile() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func int64Pointer(value int64) *int64 { return &value }

func stringPointer(value string) *string { return &value }
