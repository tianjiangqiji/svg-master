package netproxy

import (
	"golang.org/x/net/http/httpproxy"
	"net/http"
	"testing"
)

func TestNoProxyAloneStillUsesWindowsProxy(t *testing.T) {
	for _, tc := range []struct{ target, noProxy, want string }{
		{"https://auth.openai.com/api/accounts/oauth/token", "localhost,127.0.0.1,::1", "http://127.0.0.1:7897"},
		{"https://api.openai.com/v1/responses", "localhost,127.0.0.1,::1", "http://127.0.0.1:7897"},
		{"http://127.0.0.1:49452/auth/callback", "localhost,127.0.0.1,::1", ""},
		{"https://internal.example.com", ".example.com", ""},
		{"http://10.0.2.3", "10.0.0.0/8", ""},
	} {
		r, _ := http.NewRequest("GET", tc.target, nil)
		u, err := resolve(r, &httpproxy.Config{NoProxy: tc.noProxy}, "127.0.0.1:7897", "")
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.target, got, tc.want)
		}
	}
	r, _ := http.NewRequest("GET", "https://auth.openai.com", nil)
	u, err := resolve(r, &httpproxy.Config{HTTPSProxy: "http://127.0.0.1:8080", NoProxy: "localhost"}, "127.0.0.1:7897", "")
	if err != nil || u == nil || u.Port() != "8080" {
		t.Fatal("explicit proxy lost precedence", err)
	}
}

func TestWindowsProxyFormatsAndBypass(t *testing.T) {
	for _, tc := range []struct{ target, server, bypass, want string }{
		{"https://auth.openai.com", "127.0.0.1:7897", "", "http://127.0.0.1:7897"},
		{"https://api.openai.com", "http=127.0.0.1:80;https=127.0.0.1:7897", "", "http://127.0.0.1:7897"},
		{"http://127.0.0.1:5000", "127.0.0.1:7897", "", ""},
		{"http://localhost:5000", "127.0.0.1:7897", "", ""},
		{"https://internal.example.com", "127.0.0.1:7897", "*.example.com", ""},
		{"http://intranet", "127.0.0.1:7897", "<local>", ""},
		{"https://api.openai.com", "", "", ""},
	} {
		r, _ := http.NewRequest("GET", tc.target, nil)
		u, err := fromSettings(r, tc.server, tc.bypass)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		if u != nil {
			got = u.String()
		}
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.target, got, tc.want)
		}
	}
}
