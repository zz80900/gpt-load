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

// IsPattern 报告 name 是否为通配符模式。
//
// 判定只看是否含 '*'。模型名在本项目全程按字节精确比较，不存在需要转义的场景，
// 因此不需要引入模式文法：识别到 '*' 就是模式，没有 '*' 就是普通名称。
func IsPattern(name string) bool {
	return strings.IndexByte(name, '*') >= 0
}

// Match 报告 name 是否被通配符模式 pattern 匹配。
//
// '*' 匹配任意长度（含空）的字节序列，可以出现任意多次；不含 '*' 的 pattern 退化为
// 字符串相等。比较按字节进行，不做 Unicode 归一、不做大小写折叠——模型名在本项目
// 全程区分大小写。
//
// 这是全项目唯一的模式匹配定义：路由查找、展示层过滤与测试都必须经由它，否则同一条
// 模式在不同消费点会解析出不同结果。
func Match(pattern, name string) bool {
	if !IsPattern(pattern) {
		return pattern == name
	}
	// 双指针回溯：star 记录最近一个 '*' 的下标，mark 记录该 '*' 当时吞到 name 的
	// 哪个位置。失配时回退到这个 '*' 并让它多吞一个字节，因此无需递归、无需分配。
	star := -1
	mark := 0
	patternIndex, nameIndex := 0, 0
	for nameIndex < len(name) {
		switch {
		case patternIndex < len(pattern) && pattern[patternIndex] == '*':
			star = patternIndex
			mark = nameIndex
			patternIndex++
		case patternIndex < len(pattern) && pattern[patternIndex] == name[nameIndex]:
			patternIndex++
			nameIndex++
		case star >= 0:
			mark++
			nameIndex = mark
			patternIndex = star + 1
		default:
			return false
		}
	}
	// name 已耗尽，剩余的 pattern 只有全是 '*' 才算匹配（'*' 可以吞掉空串）。
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

// PatternSpecificity 返回模式的裁决度量：首个 '*' 之前的字面字节数，以及全部非 '*'
// 字节数。
//
// 用于重叠模式的确定性裁决（更长的字面前缀意味着更具体：claude-sonnet-* 比 claude-*
// 更具体；前缀相同时字面总数更多者更具体：claude-*-5 比 claude-* 更具体）。不含 '*'
// 的名称不是模式，两项都返回名称长度，使调用方无需分支。
func PatternSpecificity(pattern string) (prefixLen, literalCount int) {
	index := strings.IndexByte(pattern, '*')
	if index < 0 {
		return len(pattern), len(pattern)
	}
	return index, len(pattern) - strings.Count(pattern, "*")
}
