package llm

import (
	"encoding/json"
	"fmt"
	"sort"

	"svg-master/internal/config"
)

// Anthropic 的 thinking/signature/redacted_thinking 与 tool_use 必须原样、按序续传。
type anthropicOutputState struct {
	blocks    map[int]map[string]json.RawMessage
	args      map[int]string
	completed bool
}

func (s *anthropicOutputState) capture(p *pump, ev sseEvent) error {
	var in struct {
		Type  string                     `json:"type"`
		Index int                        `json:"index"`
		Block map[string]json.RawMessage `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
		} `json:"delta"`
	}
	if json.Unmarshal([]byte(ev.Data), &in) != nil {
		return nil
	} // 主解码器负责报告错误。
	if in.Type == "" {
		in.Type = ev.Event
	}
	switch in.Type {
	case "content_block_start":
		if s.blocks == nil {
			s.blocks = map[int]map[string]json.RawMessage{}
			s.args = map[int]string{}
		}
		if in.Block != nil {
			s.blocks[in.Index] = in.Block
		}
	case "content_block_delta":
		block := s.blocks[in.Index]
		if block == nil {
			return noRetry(fmt.Errorf("anthropic delta 缺少 content_block_start（index=%d）", in.Index))
		}
		field, delta := "", ""
		switch in.Delta.Type {
		case "text_delta":
			field, delta = "text", in.Delta.Text
		case "thinking_delta":
			field, delta = "thinking", in.Delta.Thinking
		case "signature_delta":
			field, delta = "signature", in.Delta.Signature
		case "input_json_delta":
			s.args[in.Index] += in.Delta.PartialJSON
		}
		if field != "" {
			var previous string
			_ = json.Unmarshal(block[field], &previous)
			block[field], _ = json.Marshal(previous + delta)
		}
	case "message_stop":
		var indexes []int
		for index := range s.blocks {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		var items []json.RawMessage
		for _, index := range indexes {
			block := s.blocks[index]
			if args := s.args[index]; args != "" {
				if !json.Valid([]byte(args)) {
					return noRetry(fmt.Errorf("anthropic 工具参数未完整接收（index=%d）", index))
				}
				block["input"] = json.RawMessage(args)
			}
			raw, err := json.Marshal(block)
			if err != nil {
				return noRetry(err)
			}
			items = append(items, raw)
		}
		if len(items) > 0 {
			p.send(StreamEvent{Kind: EvOutput, Output: &NativeOutput{Protocol: config.ProtoAnthropic, Items: items}})
		}
		s.completed = true
	}
	return nil
}

// Gemini 的 thoughtSignature 属于 part；按收到的顺序保存，不能用可见文字重建。
type geminiOutputState struct {
	items     []json.RawMessage
	next      int
	withID    bool
	completed bool
}

func geminiGeneratedID(index int, name string) string {
	return geminiToolID(fmt.Sprintf("call_%d_%s", index, name), name)
}

func (s *geminiOutputState) capture(ev sseEvent) error {
	var in struct {
		Candidates []struct {
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if json.Unmarshal([]byte(ev.Data), &in) != nil || len(in.Candidates) == 0 {
		return nil
	}
	candidate := in.Candidates[0]
	for _, raw := range candidate.Content.Parts {
		var part map[string]json.RawMessage
		if err := json.Unmarshal(raw, &part); err != nil {
			return noRetry(err)
		}
		if call := part["functionCall"]; len(call) > 0 && string(call) != "null" {
			var fn map[string]json.RawMessage
			if err := json.Unmarshal(call, &fn); err != nil {
				return noRetry(err)
			}
			var id, name string
			_ = json.Unmarshal(fn["id"], &id)
			_ = json.Unmarshal(fn["name"], &name)
			if id == "" && s.withID {
				fn["id"], _ = json.Marshal(geminiGeneratedID(s.next, name))
				part["functionCall"], _ = json.Marshal(fn)
				raw, _ = json.Marshal(part)
			}
			s.next++
		}
		s.items = append(s.items, raw)
	}
	if candidate.FinishReason == "STOP" {
		s.completed = true
	}
	return nil
}

func (s *geminiOutputState) emit(p *pump) {
	if len(s.items) > 0 {
		p.send(StreamEvent{Kind: EvOutput, Output: &NativeOutput{Protocol: config.ProtoGemini, Items: s.items}})
	}
}
