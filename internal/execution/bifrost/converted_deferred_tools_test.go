package bifrost

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

// convertedDeferredUpstreamBody 捕获构造后的真实上游请求体（字节级），骨架同 fast mode 用例。
func convertedDeferredUpstreamBody(t *testing.T, spec execution.AttemptSpec) []byte {
	t.Helper()
	request, err := buildConvertedResponsesRequest(spec, schemas.OpenAI)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{}
	ctx := runtime.newSDKContext(context.Background(), spec, schemas.Key{})
	defer ctx.Cancel()
	wire, sdkErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openai.ToOpenAIResponsesRequest(ctx, request), nil
	})
	if sdkErr != nil {
		t.Fatal(sdkErr)
	}
	return wire
}

// convertedDeferredUpstreamTools 取出上游 body 的 tools 数组。
func convertedDeferredUpstreamTools(t *testing.T, wire []byte) []map[string]any {
	t.Helper()
	var fields struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	return fields.Tools
}

// Claude Code 的客户端侧工具搜索只发 defer_loading、不发服务端 tool_search 声明，
// 孤儿标记必须在出站前剥离，否则上游 400 "Deferred tools require tools.tool_search"。
func TestConvertedDeferredToolsStripsOrphanedDeferLoading(t *testing.T) {
	body := `{"model":"claude-test","max_tokens":16,"tools":[{"name":"mcp__foo__bar","description":"d","input_schema":{"type":"object","properties":{}},"defer_loading":true}],"messages":[{"role":"user","content":"hi"}]}`
	wire := convertedDeferredUpstreamBody(t, convertedAnthropicSpec(body, nil))
	if bytes.Contains(wire, []byte("defer_loading")) {
		t.Fatalf("orphaned defer_loading reached upstream: %s", wire)
	}
	tools := convertedDeferredUpstreamTools(t, wire)
	if len(tools) != 1 || tools[0]["type"] != "function" || tools[0]["name"] != "mcp__foo__bar" {
		t.Fatalf("tool set changed beyond the flag: %s", wire)
	}
}

// 声明共存（正常 OpenAI 工具搜索请求）时不动作：defer_loading 与 tool_search 均按现状到达上游。
func TestConvertedDeferredToolsKeepsDeferLoadingWithToolSearch(t *testing.T) {
	body := `{"model":"claude-test","max_tokens":16,"tools":[{"name":"mcp__foo__bar","description":"d","input_schema":{"type":"object","properties":{}},"defer_loading":true},{"type":"tool_search_tool_regex_20251119","name":"tool_search_tool_regex"}],"messages":[{"role":"user","content":"hi"}]}`
	wire := convertedDeferredUpstreamBody(t, convertedAnthropicSpec(body, nil))
	tools := convertedDeferredUpstreamTools(t, wire)
	var deferredSeen, toolSearchSeen bool
	for _, tool := range tools {
		if tool["name"] == "mcp__foo__bar" && tool["defer_loading"] == true {
			deferredSeen = true
		}
		if tool["type"] == "tool_search" {
			toolSearchSeen = true
		}
	}
	if !deferredSeen || !toolSearchSeen {
		t.Fatalf("defer_loading + tool_search coexistence changed: %s", wire)
	}
}

// 无 defer_loading 的普通请求零改动：修正函数不触碰请求对象，出站 body 不受影响。
func TestConvertedDeferredToolsPlainRequestUnchanged(t *testing.T) {
	spec := convertedAnthropicSpec(`{"model":"claude-test","max_tokens":16,"tools":[{"name":"lookup","description":"d","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":"hi"}]}`, nil)
	request, err := buildConvertedResponsesRequest(spec, schemas.OpenAI)
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	stripOrphanedDeferredToolFlags(request)
	after, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("plain request mutated: before %s after %s", before, after)
	}
	if wire := convertedDeferredUpstreamBody(t, spec); bytes.Contains(wire, []byte("defer_loading")) {
		t.Fatalf("plain request body changed: %s", wire)
	}
}

// SDK 的 keepDeferLoading 处理只看顶层工具，namespace 子工具的 defer_loading 会裸奔到上游；
// 出站构造必须递归剥离，同时保留 namespace 工具本身。
func TestConvertedDeferredToolsNamespaceWireRecursion(t *testing.T) {
	spec := execution.AttemptSpec{
		RouteMode: execution.RouteConverted, ClientProtocol: protocol.OpenAIResponses,
		Operation: execution.OperationResponsesCreate, UpstreamModel: "gpt-6.1-sol",
		Body: []byte(`{"model":"client-model","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"namespace","name":"helpers","tools":[{"type":"function","name":"search","parameters":{"type":"object"},"defer_loading":true}]}]}`),
	}
	wire := convertedDeferredUpstreamBody(t, spec)
	if bytes.Contains(wire, []byte("defer_loading")) {
		t.Fatalf("namespace sub-tool defer_loading reached upstream: %s", wire)
	}
	if !bytes.Contains(wire, []byte(`"namespace"`)) {
		t.Fatalf("namespace tool dropped: %s", wire)
	}
}

// 原生路由不介入：复用同一构造的非转换路由必须保持 defer_loading 原样。
func TestConvertedDeferredToolsLeavesNativeRouteUnchanged(t *testing.T) {
	spec := execution.AttemptSpec{
		RouteMode: execution.RouteNative, ClientProtocol: protocol.OpenAIResponses,
		Operation: execution.OperationResponsesCreate, UpstreamModel: "gpt-6.1-sol",
		Body: []byte(`{"model":"gpt-6.1-sol","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"defer_loading":true}]}`),
	}
	wire := convertedDeferredUpstreamBody(t, spec)
	tools := convertedDeferredUpstreamTools(t, wire)
	if len(tools) != 1 || tools[0]["defer_loading"] != true {
		t.Fatalf("native route intervened: %s", wire)
	}
}

// namespace 子工具与顶层同构：递归判定与递归剥离；顶层声明存在时同样不动作。
func TestConvertedDeferredToolsNamespaceRecursion(t *testing.T) {
	deferred := true
	namespaceWithDeferred := func() schemas.ResponsesTool {
		return schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeNamespace,
			Name: schemas.Ptr("helpers"),
			ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
				Tools: []schemas.ResponsesTool{
					{Type: schemas.ResponsesToolTypeFunction, Name: schemas.Ptr("search"), DeferLoading: &deferred},
				},
			},
		}
	}
	request := func(tools ...schemas.ResponsesTool) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Params: &schemas.ResponsesParameters{Tools: tools},
		}
	}

	orphaned := request(namespaceWithDeferred())
	stripOrphanedDeferredToolFlags(orphaned)
	if orphaned.Params.Tools[0].ResponsesToolNamespace.Tools[0].DeferLoading != nil {
		t.Fatal("namespace sub-tool defer_loading not stripped")
	}

	declared := request(namespaceWithDeferred(), schemas.ResponsesTool{Type: schemas.ResponsesToolTypeToolSearch})
	stripOrphanedDeferredToolFlags(declared)
	kept := declared.Params.Tools[0].ResponsesToolNamespace.Tools[0].DeferLoading
	if kept == nil || !*kept {
		t.Fatal("namespace sub-tool defer_loading stripped despite tool_search declaration")
	}
}
