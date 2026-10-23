package llm

import (
	"context"

	"svg-master/internal/config"
)

// NewClient 按协议构造客户端。baseURL 已去尾斜杠，apiKey 为明文。
// 未知协议返回一个 stub client：Protocol() 原样返回协议名，
// Stream() 立刻发一条中文错误事件，便于前端把配置错误显示出来。
func NewClient(protocol, baseURL, apiKey string, t Transport) Client {
	nt := normalizeTransport(t)
	switch protocol {
	case config.ProtoOpenAIResp:
		return &openaiResponses{base: baseURL, key: apiKey, t: nt}
	case config.ProtoAnthropic:
		return &anthropicClient{base: baseURL, key: apiKey, t: nt}
	case config.ProtoGemini:
		return &geminiClient{base: baseURL, key: apiKey, t: nt}
	case config.ProtoOpenAIChat, "":
		return &openaiChat{base: baseURL, key: apiKey, t: nt}
	default:
		return &stubClient{protocol: protocol}
	}
}

// stubClient 未知协议的兜底实现。
type stubClient struct{ protocol string }

func (c *stubClient) Protocol() string { return c.protocol }

func (c *stubClient) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Kind: EvError, Err: "不支持的协议：" + c.protocol + "（可选：openai-completions / openai-responses / anthropic-messages / google-generative-ai）"}
	close(ch)
	return ch, nil
}

// newClientFor 内部用：按协议取具体实现，并给出中文错误。
func newClientFor(protocol, baseURL, apiKey string, t Transport) (Client, error) {
	c := NewClient(protocol, baseURL, apiKey, t)
	if _, ok := c.(*stubClient); ok {
		return nil, &protocolError{protocol: protocol}
	}
	return c, nil
}

type protocolError struct{ protocol string }

func (e *protocolError) Error() string {
	return "不支持的协议：" + e.protocol
}
