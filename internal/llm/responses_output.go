package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"svg-master/internal/config"
)

type respResponseIn struct {
	ServiceTier string            `json:"service_tier"`
	Status      string            `json:"status"`
	Output      []json.RawMessage `json:"output"`
	Usage       *respUsageIn      `json:"usage"`
	Error       *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func (r *respResponseIn) checkStatus(kind string) error {
	if r.Error != nil || kind == "response.failed" || r.Status == "failed" {
		msg := "响应失败"
		if r.Error != nil {
			msg = r.Error.Message
		}
		code := ""
		if r.Error != nil {
			code = r.Error.Code
		}
		if code == "subscription_sharing_usage_limit_exceeded" || code == "subscription_sharing_usage_unavailable" {
			return noRetry(fmt.Errorf("ChatGPT 订阅额度已用完或当前不可用，请打开「管理供应商 → 管理额度」查看；不会自动切换到 API 付费"))
		}
		return remoteError("openai-responses", code, msg)
	}
	if kind == "response.incomplete" || r.Status == "incomplete" || r.IncompleteDetails != nil {
		reason := "未知原因"
		if r.IncompleteDetails != nil {
			reason = r.IncompleteDetails.Reason
		}
		return noRetry(fmt.Errorf("openai-responses 响应未完成：%s；请检查输出 token 上限或渠道限制", reason))
	}
	if r.Status != "" && r.Status != "completed" {
		return noRetry(fmt.Errorf("openai-responses 尚未完成：status=%s", r.Status))
	}
	return nil
}

// toolSnapshot 修正网关省略 added/delta 时的名称、call_id 和完整参数。
func (s *respState) toolSnapshot(p *pump, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var item respItemIn
	if err := json.Unmarshal(raw, &item); err != nil {
		return noRetry(fmt.Errorf("解析 Responses output item 失败：%w", err))
	}
	if item.Type != "function_call" {
		return nil
	}
	ref := s.ensure(&item)
	p.send(StreamEvent{Kind: EvToolStart, Index: ref.index, ID: ref.callID, Name: ref.name})
	if item.Arguments != "" && item.Arguments != ref.args {
		ref.args = item.Arguments
		p.send(StreamEvent{Kind: EvToolDelta, Index: ref.index, ID: ref.callID, Text: ref.args, Replace: true})
	}
	return nil
}

func (s *respState) finishOutput(p *pump) error {
	var text, thinking strings.Builder
	for _, raw := range s.items {
		if err := s.toolSnapshot(p, raw); err != nil {
			return err
		}
		var item respItemIn
		if err := json.Unmarshal(raw, &item); err != nil {
			return noRetry(fmt.Errorf("解析 Responses output item 失败：%w", err))
		}
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				if part.Type == "refusal" {
					return noRetry(fmt.Errorf("模型拒绝生成：%s", truncateRunes(part.Refusal, errCharLimit)))
				}
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		case "reasoning":
			for _, part := range item.Summary {
				thinking.WriteString(part.Text)
			}
		}
	}
	// 完成快照是权威结果；只补缺失后缀，正常增量不重复输出。
	if full := text.String(); strings.HasPrefix(full, s.text) && len(full) > len(s.text) {
		p.send(StreamEvent{Kind: EvText, Text: full[len(s.text):]})
		s.text = full
	}
	if full := thinking.String(); strings.HasPrefix(full, s.thinking) && len(full) > len(s.thinking) {
		p.send(StreamEvent{Kind: EvThinking, Text: full[len(s.thinking):]})
		s.thinking = full
	}
	if len(s.items) > 0 {
		p.send(StreamEvent{Kind: EvOutput, Output: &NativeOutput{Protocol: config.ProtoOpenAIResp, Items: s.items}})
	}
	return nil
}
