package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"svg-master/internal/config"
	"svg-master/internal/llm"
)

func TestIncompleteResponseKeepsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"usage\":{\"input_tokens\":100,\"output_tokens\":50,\"input_tokens_details\":{\"cached_tokens\":80},\"total_tokens\":150}}}\n\n")
	}))
	defer srv.Close()
	deps := testDeps(&fakeClient{}, nil)
	deps.Client = llm.NewClient(config.ProtoOpenAIResp, srv.URL, "test", llm.Transport{Timeout: time.Second, IdleTimeout: time.Second})
	stats, err := New(deps).Run(context.Background(), Task{Prompt: "draw"})
	if err == nil || stats.InputTokens != 100 || stats.OutputTokens != 50 || stats.CacheRead != 80 || stats.Tokens != 150 {
		t.Fatalf("失败响应的计费用量被丢失：%v %+v", err, stats)
	}
}
