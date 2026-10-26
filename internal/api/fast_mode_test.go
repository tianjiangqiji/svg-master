package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFastModeReachesResponsesAndReportsActualTier(t *testing.T) {
	for _, tc := range []struct {
		name     string
		saved    bool
		override *bool
		want     string
	}{
		{"off-by-default", false, nil, ""},
		{"saved-fast", true, nil, "priority"},
		{"explicit-off", true, func() *bool { b := false; return &b }(), ""},
		{"explicit-on", false, func() *bool { b := true; return &b }(), "priority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodySeen := make(chan map[string]any, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				json.NewDecoder(r.Body).Decode(&body)
				bodySeen <- body
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"service_tier\":\"default\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n")
			}))
			defer upstream.Close()
			s, srv := newTestAPI(t, upstream.URL)
			st := s.store.Settings()
			st.FastMode = tc.saved
			st.Agent.MaxSteps = 1
			if err := s.store.Save(st); err != nil {
				t.Fatal(err)
			}
			in := map[string]any{"prompt": "test", "session": "fast-test", "genMode": "quick"}
			if tc.override != nil {
				in["fastMode"] = *tc.override
			}
			resp := apiPost(t, srv, "/api/chat", in)
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != 200 {
				t.Fatalf("HTTP %d: %s", resp.StatusCode, raw)
			}
			body := <-bodySeen
			got, _ := body["service_tier"].(string)
			if got != tc.want {
				t.Fatalf("tier=%q, want %q", got, tc.want)
			}
			if tc.want == "" {
				if _, ok := body["service_tier"]; ok {
					t.Fatal("disabled Fast changed existing request")
				}
			}
			if !strings.Contains(string(raw), `"kind":"service_tier","text":"default"`) {
				t.Fatalf("actual server tier missing: %s", raw)
			}
			if tc.want != "" && !strings.Contains(string(raw), "本轮按标准档位处理") {
				t.Fatal("server fallback was not reported")
			}
		})
	}
}
