package bifrost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"

	"gpt-load/internal/execution"
	"gpt-load/internal/protocol"
)

// convertedFastModeUpstreamBody 捕获构造后的真实上游请求体。
func convertedFastModeUpstreamBody(t *testing.T, spec execution.AttemptSpec) map[string]any {
	t.Helper()
	request, err := buildConvertedResponsesRequest(spec, schemas.OpenAI)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{}
	ctx := runtime.newSDKContext(context.Background(), spec, schemas.Key{})
	defer ctx.Cancel()
	// 打开扩展参数透传：否则该路径默认不合并 extras，"上游 body 不含 speed" 的断言将形同虚设。
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	wire, sdkErr := providerUtils.CheckContextAndGetRequestBody(ctx, request, func() (providerUtils.RequestBodyWithExtraParams, error) {
		return openai.ToOpenAIResponsesRequest(ctx, request), nil
	})
	if sdkErr != nil {
		t.Fatal(sdkErr)
	}
	var fields map[string]any
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

// convertedAnthropicSpec 直构 anthropic → openai-responses 的转换路由请求。
func convertedAnthropicSpec(body string, configured []string) execution.AttemptSpec {
	return execution.AttemptSpec{
		RouteMode: execution.RouteConverted, ClientProtocol: protocol.Anthropic,
		Operation: execution.OperationChatCompletion, UpstreamModel: "gpt-6.1-sol",
		Body: []byte(body), ConfiguredParameters: configured,
	}
}

func TestConvertedFastModeMapsSpeedToServiceTier(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		configured []string
		wantTier   any
	}{
		{
			name:     "client fast speed",
			body:     `{"model":"claude-test","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"hi"}]}`,
			wantTier: "priority",
		},
		{
			name: "client standard speed",
			body: `{"model":"claude-test","max_tokens":16,"speed":"standard","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:     "speed wins over mapped anthropic service tier",
			body:     `{"model":"claude-test","max_tokens":16,"speed":"fast","service_tier":"auto","messages":[{"role":"user","content":"hi"}]}`,
			wantTier: "priority",
		},
		{
			name: "anthropic service tier semantics unchanged without speed",
			body: `{"model":"claude-test","max_tokens":16,"service_tier":"standard_only","messages":[{"role":"user","content":"hi"}]}`,
			// R4：不改变 anthropic service_tier 的既有映射（standard_only → default）。
			wantTier: "default",
		},
		{
			name:       "parameter override injection",
			body:       `{"model":"claude-test","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"hi"}]}`,
			configured: []string{"speed"},
			wantTier:   "priority",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := convertedFastModeUpstreamBody(t, convertedAnthropicSpec(tc.body, tc.configured))
			if value, exists := fields["speed"]; exists {
				t.Fatalf("speed leaked to upstream body: %#v", value)
			}
			tier := fields["service_tier"]
			if tier != tc.wantTier {
				t.Fatalf("upstream service_tier = %#v, want %#v (body %#v)", tier, tc.wantTier, fields)
			}
		})
	}
}

func TestConvertedFastModeLeavesNativeResponsesRouteUnchanged(t *testing.T) {
	spec := execution.AttemptSpec{
		RouteMode: execution.RouteNative, ClientProtocol: protocol.OpenAIResponses,
		Operation: execution.OperationResponsesCreate, UpstreamModel: "gpt-6.1-sol",
		ConfiguredParameters: []string{"speed"},
		Body:                 []byte(`{"model":"gpt-6.1-sol","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"speed":"fast"}`),
	}
	fields := convertedFastModeUpstreamBody(t, spec)
	if fields["service_tier"] != nil || fields["speed"] != nil {
		t.Fatalf("native responses route changed: %#v", fields)
	}
}

// 参数覆盖注入的 speed 会落到 extras 恢复路径（客户端协议本身不认识该字段）；
// 非字符串取值按 R3 只清理、不设置 tier。
func TestConvertedFastModeAppliesOverrideExtras(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantTier any
	}{
		{
			name:     "override fast",
			body:     `{"model":"client-model","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"speed":"fast"}`,
			wantTier: "priority",
		},
		{
			name: "override non-string speed",
			body: `{"model":"client-model","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"speed":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := execution.AttemptSpec{
				RouteMode: execution.RouteConverted, ClientProtocol: protocol.OpenAIResponses,
				Operation: execution.OperationResponsesCreate, UpstreamModel: "gpt-6.1-sol",
				Body: []byte(tc.body), ConfiguredParameters: []string{"speed"},
			}
			fields := convertedFastModeUpstreamBody(t, spec)
			if value, exists := fields["speed"]; exists {
				t.Fatalf("speed leaked to upstream body: %#v", value)
			}
			if tier := fields["service_tier"]; tier != tc.wantTier {
				t.Fatalf("upstream service_tier = %#v, want %#v (body %#v)", tier, tc.wantTier, fields)
			}
		})
	}
}

// DeepSeek 的 anthropic 端点走原生路由，其 anthropic 出站依赖 ExtraParams["speed"] 还原 fast mode，
// 转换层的映射绝不能碰它。
func TestConvertedFastModeLeavesNativeAnthropicRouteUnchanged(t *testing.T) {
	spec := execution.AttemptSpec{
		RouteMode: execution.RouteNative, ClientProtocol: protocol.Anthropic,
		Operation: execution.OperationChatCompletion, UpstreamModel: "deepseek-chat",
		Body: []byte(`{"model":"claude-test","max_tokens":16,"speed":"fast","messages":[{"role":"user","content":"hi"}]}`),
	}
	request, err := buildConvertedResponsesRequest(spec, schemas.DeepSeek)
	if err != nil {
		t.Fatal(err)
	}
	if request.Params.ServiceTier != nil || request.Params.ExtraParams["speed"] != "fast" {
		t.Fatalf("native DeepSeek anthropic route changed: %+v", request.Params)
	}
}
