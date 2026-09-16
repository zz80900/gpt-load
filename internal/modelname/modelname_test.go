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
