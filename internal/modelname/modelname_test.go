package modelname

import "testing"

func TestBaseStripsContextSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain name", value: "claude-deepseek-flash", want: "claude-deepseek-flash"},
		{name: "empty", value: "", want: ""},
		{name: "upper suffix", value: "claude-deepseek-flash[1M]", want: "claude-deepseek-flash"},
		{name: "lower suffix", value: "claude-deepseek-flash[1m]", want: "claude-deepseek-flash"},
		{name: "suffix only upper", value: "[1M]", want: "[1M]"},
		{name: "suffix only lower", value: "[1m]", want: "[1m]"},
		{name: "repeated suffix", value: "[1M][1M]", want: "[1M]"},
		{name: "suffix in middle", value: "xx[1M]yy", want: "xx[1M]yy"},
		{name: "suffix as prefix", value: "[1m]xxxx", want: "[1m]xxxx"},
		{name: "unrelated bracket suffix", value: "claude-deepseek-flash[2M]", want: "claude-deepseek-flash[2M]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Base(test.value); got != test.want {
				t.Fatalf("Base(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestIsPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "empty is not a pattern", value: "", want: false},
		{name: "plain name is not a pattern", value: "claude-opus-5", want: false},
		{name: "context suffix is not a pattern", value: "claude-opus-5[1m]", want: false},
		{name: "trailing wildcard", value: "claude-*", want: true},
		{name: "leading wildcard", value: "*-sonnet", want: true},
		{name: "middle wildcard", value: "claude-*-5", want: true},
		{name: "bare wildcard", value: "*", want: true},
		{name: "wildcard before context suffix", value: "claude-*[1m]", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := IsPattern(test.value); got != test.want {
				t.Fatalf("IsPattern(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		// 不含 '*' 的模式退化为字符串相等。
		{name: "literal pattern matches itself", pattern: "claude-opus-5", value: "claude-opus-5", want: true},
		{name: "literal pattern is case sensitive", pattern: "claude-opus-5", value: "Claude-Opus-5", want: false},
		{name: "literal pattern rejects longer name", pattern: "claude", value: "claude-opus-5", want: false},
		{name: "literal pattern rejects prefix sharing", pattern: "claude-opus-5", value: "claude-opus-5[1m]", want: false},

		// 前缀模式：Claude 适配的核心场景。
		{name: "trailing wildcard matches any suffix", pattern: "claude-*", value: "claude-opus-5", want: true},
		{name: "trailing wildcard matches another suffix", pattern: "claude-*", value: "claude-sonnet-5", want: true},
		{name: "trailing wildcard matches empty suffix", pattern: "claude-*", value: "claude-", want: true},
		{name: "trailing wildcard matches suffixed request", pattern: "claude-*[1m]", value: "claude-opus-5[1m]", want: true},
		{name: "trailing wildcard does not match other prefix", pattern: "claude-*", value: "gpt-4o", want: false},
		{name: "trailing wildcard does not match shorter name", pattern: "claude-*", value: "claude", want: false},
		{name: "exact pattern wins no match for suffixed request", pattern: "claude-*[1m]", value: "claude-opus-5", want: false},

		// 后缀与中缀模式。
		{name: "leading wildcard matches prefix", pattern: "*-sonnet", value: "claude-sonnet", want: true},
		{name: "leading wildcard rejects other tail", pattern: "*-sonnet", value: "claude-opus", want: false},
		{name: "middle wildcard matches", pattern: "claude-*-5", value: "claude-opus-5", want: true},
		{name: "middle wildcard rejects missing tail", pattern: "claude-*-5", value: "claude-opus-4", want: false},

		// 多个 '*'：回溯逻辑必须让每个 '*' 重新尝试。
		{name: "two wildcards match", pattern: "claude-*-*-5", value: "claude-opus-4-5", want: true},
		{name: "two wildcards match adjacent", pattern: "a*b*c", value: "abc", want: true},
		{name: "two wildcards backtrack", pattern: "a*b*c", value: "axxbyyc", want: true},
		{name: "two wildcards reject missing literal", pattern: "a*b*c", value: "axxbyy", want: false},

		// 边界。
		{name: "bare wildcard matches anything", pattern: "*", value: "claude-opus-5", want: true},
		{name: "bare wildcard matches empty name", pattern: "*", value: "", want: true},
		{name: "empty pattern matches empty name", pattern: "", value: "", want: true},
		{name: "empty pattern rejects non empty name", pattern: "", value: "claude", want: false},
		{name: "wildcard only pattern rejects nothing to consume", pattern: "claude-*", value: "", want: false},
		{name: "adjacent wildcards collapse", pattern: "claude-**", value: "claude-opus-5", want: true},
		{name: "name containing literal wildcard", pattern: "claude-*[1m]", value: "claude-*[1m]", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Match(test.pattern, test.value); got != test.want {
				t.Fatalf("Match(%q, %q) = %v, want %v", test.pattern, test.value, got, test.want)
			}
		})
	}
}

// TestBaseTreatsWildcardsAsOrdinaryBytes 钉住 Base 与通配符正交：后缀剥离只看结尾的
// [1M]/[1m]，'*' 在中间或结尾时都不影响剥离结果。这是「别名 claude-*[1m] 同时以自身
// 与基名 claude-* 注册」的前提。
func TestBaseTreatsWildcardsAsOrdinaryBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "wildcard without suffix is identity", value: "claude-*", want: "claude-*"},
		{name: "wildcard strips lower suffix", value: "claude-*[1m]", want: "claude-*"},
		{name: "wildcard strips upper suffix", value: "claude-*[1M]", want: "claude-*"},
		{name: "bare wildcard is identity", value: "*", want: "*"},
		{name: "middle wildcard is identity", value: "claude-*-5", want: "claude-*-5"},
		{name: "wildcard alone before suffix", value: "*[1m]", want: "*"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Base(test.value); got != test.want {
				t.Fatalf("Base(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestPatternSpecificity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		pattern      string
		wantPrefix   int
		wantLiterals int
	}{
		{name: "bare wildcard", pattern: "*", wantPrefix: 0, wantLiterals: 0},
		{name: "adjacent wildcards", pattern: "**", wantPrefix: 0, wantLiterals: 0},
		{name: "trailing wildcard", pattern: "claude-*", wantPrefix: 7, wantLiterals: 7},
		{name: "longer literal prefix", pattern: "claude-sonnet-*", wantPrefix: 14, wantLiterals: 14},
		{name: "middle wildcard", pattern: "claude-*-5", wantPrefix: 7, wantLiterals: 9},
		{name: "leading wildcard", pattern: "*-sonnet", wantPrefix: 0, wantLiterals: 7},
		{name: "no wildcard", pattern: "claude-opus-5", wantPrefix: 13, wantLiterals: 13},
		{name: "empty", pattern: "", wantPrefix: 0, wantLiterals: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			prefix, literals := PatternSpecificity(test.pattern)
			if prefix != test.wantPrefix || literals != test.wantLiterals {
				t.Fatalf(
					"PatternSpecificity(%q) = (%d, %d), want (%d, %d)",
					test.pattern, prefix, literals, test.wantPrefix, test.wantLiterals,
				)
			}
		})
	}
}

func TestAllowsAcceptsExactAndBaseNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allowed []string
		value   string
		want    bool
	}{
		{name: "empty set rejects plain name", allowed: nil, value: "xxxx", want: false},
		{name: "empty set rejects suffixed name", allowed: nil, value: "xxxx[1M]", want: false},
		{name: "exact plain name", allowed: []string{"xxxx"}, value: "xxxx", want: true},
		{name: "suffixed whitelist accepts exact", allowed: []string{"xxxx[1M]"}, value: "xxxx[1M]", want: true},
		{name: "suffixed whitelist accepts base", allowed: []string{"xxxx[1M]"}, value: "xxxx", want: true},
		{name: "lower suffixed whitelist accepts base", allowed: []string{"xxxx[1m]"}, value: "xxxx", want: true},
		{name: "base whitelist accepts suffixed", allowed: []string{"xxxx"}, value: "xxxx[1M]", want: true},
		{name: "base whitelist accepts lower suffixed", allowed: []string{"xxxx"}, value: "xxxx[1m]", want: true},
		{name: "unrelated name rejected", allowed: []string{"xxxx[1M]"}, value: "yyyy", want: false},
		{name: "sibling suffix rejected", allowed: []string{"xxxx[1M]"}, value: "xxxx[2M]", want: false},
		{name: "prefix sharing rejected", allowed: []string{"xxxx[1M]"}, value: "xxxxx", want: false},
		{name: "base whitelist rejects other base", allowed: []string{"xxxx"}, value: "yyyy[1M]", want: false},
		{name: "base whitelist accepts suffix only alias", allowed: []string{"[1M]"}, value: "[1M]", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			allowed := make(map[string]struct{}, len(test.allowed))
			for _, value := range test.allowed {
				allowed[value] = struct{}{}
			}
			if got := Allows(allowed, test.value); got != test.want {
				t.Fatalf("Allows(%v, %q) = %v, want %v", test.allowed, test.value, got, test.want)
			}
		})
	}
}
