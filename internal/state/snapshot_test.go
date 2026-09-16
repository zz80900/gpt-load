package state

import (
	"encoding/json"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"gpt-load/internal/accessquota"
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
