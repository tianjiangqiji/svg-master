// Go 侧单元验证：CanonicalBaseURL 归一化全部用例
package config

import "testing"

func TestCanonicalBaseURLNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://api.test.com", "https://api.test.com/v1"},
		{"https://api.test.com/", "https://api.test.com/v1"},
		{"https://api.test.com//", "https://api.test.com/v1"},
		{"https://api.test.com/v1", "https://api.test.com/v1"},
		{"https://api.test.com/v1/", "https://api.test.com/v1"},
		{"https://api.test.com/v1//", "https://api.test.com/v1"},
		{"https://api.test.com/api/v1/", "https://api.test.com/api/v1"},
		{"  https://api.test.com  ", "https://api.test.com/v1"},
		{"api.test.com", "https://api.test.com/v1"},
		{"api.test.com/v1/", "https://api.test.com/v1"},
	}
	for _, c := range cases {
		got := CanonicalBaseURL(ProtoOpenAIChat, c.in)
		if got != c.want {
			t.Errorf("CanonicalBaseURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// Gemini 走 /v1beta
	if got := CanonicalBaseURL(ProtoGemini, "https://g.test.com"); got != "https://g.test.com/v1beta" {
		t.Errorf("gemini: got %q", got)
	}
	if got := CanonicalBaseURL(ProtoGemini, "https://g.test.com/v1beta/"); got != "https://g.test.com/v1beta" {
		t.Errorf("gemini trailing slash: got %q", got)
	}
	if got := CanonicalBaseURL(ProtoAnthropic, "https://a.test.com/v1/"); got != "https://a.test.com/v1" {
		t.Errorf("anthropic: got %q", got)
	}
}
