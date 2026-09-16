package dialect

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

// AnthropicContext1MBeta 是 1M 上下文的 beta 标识符。
const AnthropicContext1MBeta = "context-1m-2025-08-07"

// anthropicBetaHeader 承载 beta 声明的请求头。http.Header.Values 内部做 MIME 规范化，各种大小写形式等价。
const anthropicBetaHeader = "Anthropic-Beta"

type anthropicBetaRoot struct {
	Betas []string `json:"betas"`
}

// inspectAnthropicBetas 收集客户端声明的 beta 能力，来源为请求头与请求体顶层 betas 数组，二者取并集。
//
// 采集是旁路观测：任一来源解析失败只丢弃该来源，绝不返回错误，以免影响请求本身。
// 结果去重并排序，使同一组声明无论出现顺序如何都得到同一个字符串。
// 不做大小写归一——客户端发出的实际大小写就是事实，上游按精确匹配决定 beta 是否生效。
func inspectAnthropicBetas(header http.Header, body []byte) []string {
	seen := make(map[string]struct{})
	for _, value := range header.Values(anthropicBetaHeader) {
		for _, beta := range strings.Split(value, ",") {
			addAnthropicBeta(seen, beta)
		}
	}

	var root anthropicBetaRoot
	if err := json.Unmarshal(body, &root); err == nil {
		for _, beta := range root.Betas {
			addAnthropicBeta(seen, beta)
		}
	}

	if len(seen) == 0 {
		return nil
	}
	betas := make([]string, 0, len(seen))
	for beta := range seen {
		betas = append(betas, beta)
	}
	sort.Strings(betas)
	return betas
}

func addAnthropicBeta(seen map[string]struct{}, value string) {
	if beta := strings.TrimSpace(value); beta != "" {
		seen[beta] = struct{}{}
	}
}
