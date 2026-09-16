// Package modelname owns canonicalization of client-facing model names.
//
// 本包刻意不依赖任何 gpt-load/internal 下的包：state 包依赖 parameteroverride，
// 而 parameteroverride 也需要后缀归一，后缀工具放在 state 里会形成反向依赖。
package modelname

import "strings"

// ContextSuffixes 是客户端用于声明扩展上下文窗口的模型名后缀。
//
// Claude Code 从模型列表里识别到后缀后启用大上下文，但发起请求时会把后缀去掉，
// 因此「带后缀名」与「去后缀基名」指向同一上游模型，必须都能路由。两种大小写
// 形态并列列出，等价于大小写不敏感匹配。
var ContextSuffixes = [...]string{"[1M]", "[1m]"}

// Base 返回剥离上下文后缀后的模型名；不含后缀时原样返回。
//
// 长度必须严格大于后缀，因此 [1M] 自身不会被剥成空串：基名为空时不应产生派生名，
// 否则路由索引里会多出一个永远无意义的空键，模型页也会多出一条记录。
func Base(name string) string {
	for _, suffix := range ContextSuffixes {
		if len(name) > len(suffix) && strings.HasSuffix(name, suffix) {
			return name[:len(name)-len(suffix)]
		}
	}
	return name
}

// Allows 报告 allowed 集合是否接受 name：精确命中，或按上下文后缀归一后命中。
//
// 两个方向都要覆盖：客户端可能把白名单里的带后缀名剥成基名发起请求，规则也可能
// 反过来写基名而请求带后缀。空集合的语义由调用方保留（「未配置即不限制」），
// 本函数不做特判，避免职责发散。
func Allows(allowed map[string]struct{}, name string) bool {
	if _, exact := allowed[name]; exact {
		return true
	}
	base := Base(name)
	if base != name {
		_, allowedBase := allowed[base]
		return allowedBase
	}
	// name 本身是基名，候选只能来自后缀变体。逐项 map 查询，不遍历白名单。
	for _, suffix := range ContextSuffixes {
		if _, allowedSuffixed := allowed[name+suffix]; allowedSuffixed {
			return true
		}
	}
	return false
}
