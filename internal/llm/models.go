package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"svg-master/internal/config"
)

// modelsTimeout 模型列表抓取的总超时（与任务契约一致：15s）。
const modelsTimeout = 15 * time.Second

// ---------------- FetchRemoteModels ----------------

// FetchRemoteModels 抓取远端模型列表。
func FetchRemoteModels(protocol, baseURL, apiKey string, t Transport) ([]config.ModelEntry, error) {
	base := NormalizeBaseURL(protocol, baseURL)
	if base == "" {
		return nil, fmt.Errorf("Base URL 为空，无法获取模型列表")
	}

	ctx, cancel := context.WithTimeout(context.Background(), modelsTimeout)
	defer cancel()

	// 模型列表是一次性请求，重试没有意义，直接把次数压到 0 省时间。
	nt := normalizeTransport(t)
	nt.MaxRetries = 0
	d := newDoer(nt)

	var url string
	headers := map[string]string{}
	switch protocol {
	case config.ProtoAnthropic:
		url = anthropicModelsURL(base)
		headers["x-api-key"] = apiKey
		headers["anthropic-version"] = anthropicVersion
	case config.ProtoGemini:
		url = geminiModelsURL(base)
		headers["x-goog-api-key"] = apiKey
	default:
		url = strings.TrimRight(base, "/") + "/models"
		headers["Authorization"] = "Bearer " + apiKey
	}

	resp, err := d.getJSON(ctx, url, headers)
	if err != nil {
		return nil, fmt.Errorf("获取模型列表失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError("获取模型列表失败", resp)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取模型列表响应失败：%w", err)
	}

	var out []config.ModelEntry
	switch protocol {
	case config.ProtoGemini:
		out, err = parseGeminiModels(raw)
	default:
		out, err = parseOpenAIStyleModels(raw)
	}
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("获取模型列表成功但结果为空（%s）", url)
	}
	return out, nil
}

// anthropicModelsURL base 已带版本段时直接拼 /models，否则补 /v1。
func anthropicModelsURL(base string) string {
	base = strings.TrimRight(base, "/")
	if versionSegmentRe.MatchString(base) {
		return base + "/models"
	}
	return base + "/v1/models"
}

// geminiModelsURL base 已带 /v1beta 时直接拼 /models，否则补 /v1beta。
func geminiModelsURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1beta") || strings.HasSuffix(base, "/v1") || strings.HasSuffix(base, "/v1alpha") {
		return base + "/models"
	}
	return base + "/v1beta/models"
}

var versionSegmentRe = regexp.MustCompile(`/v\d+[a-z]*$`)

// parseOpenAIStyleModels 解析 {data:[{id}]} 或裸数组 [{id}]（Anthropic 用 {data:[{id,display_name}]}）。
func parseOpenAIStyleModels(raw []byte) ([]config.ModelEntry, error) {
	type item struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	}
	var wrapper struct {
		Data []item `json:"data"`
	}
	items := []item{}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Data) > 0 {
		items = wrapper.Data
	} else {
		var arr []item
		if err2 := json.Unmarshal(raw, &arr); err2 != nil {
			return nil, fmt.Errorf("解析模型列表失败：%v", err)
		}
		items = arr
	}

	seen := map[string]bool{}
	out := make([]config.ModelEntry, 0, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.ID)
		if id == "" {
			id = strings.TrimSpace(it.Name)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(it.DisplayName)
		if name == "" {
			name = AutoModelName(id)
		}
		out = append(out, config.ModelEntry{ID: id, Name: name})
	}
	return out, nil
}

// parseGeminiModels 解析 {models:[{name:"models/gemini-2.5-pro", displayName}]}。
func parseGeminiModels(raw []byte) ([]config.ModelEntry, error) {
	var wrapper struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("解析 gemini 模型列表失败：%v", err)
	}
	seen := map[string]bool{}
	out := make([]config.ModelEntry, 0, len(wrapper.Models))
	for _, m := range wrapper.Models {
		id := strings.TrimPrefix(strings.TrimSpace(m.Name), "models/")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			name = AutoModelName(id)
		}
		out = append(out, config.ModelEntry{ID: id, Name: name})
	}
	return out, nil
}

// ---------------- 能力推断 ----------------

// 有序规则：命中第一条即停（contextWindow, maxTokens）。
type capRule struct {
	match  func(string) bool
	window int
	maxTok int
}

func containsAny(needles ...string) func(string) bool {
	return func(id string) bool {
		for _, n := range needles {
			if strings.Contains(id, n) {
				return true
			}
		}
		return false
	}
}

var oSeriesRe = regexp.MustCompile(`^o[1-4](-|$)`)

var capRules = []capRule{
	{containsAny("gemini-1.5"), 1048576, 8192},
	{containsAny("gemini-2.5", "gemini-2.0", "gemini-exp"), 1048576, 65536},
	{containsAny("gemini-3", "gemini-2"), 1048576, 65536},
	{containsAny("claude-3-7", "claude-3.7", "claude-sonnet-4", "claude-opus-4", "claude-4"), 200000, 64000},
	{containsAny("claude-3-5"), 200000, 8192},
	{containsAny("claude"), 200000, 32000},
	{containsAny("gpt-4.1"), 1047576, 32768},
	{containsAny("gpt-4o", "gpt-4-turbo", "gpt-4-vision"), 128000, 16384},
	{func(id string) bool { return oSeriesRe.MatchString(id) || strings.Contains(id, "gpt-5") }, 400000, 128000},
	{containsAny("deepseek-v4", "deepseek-r2"), 1048576, 131072}, // V4 系已达 1M
	{containsAny("deepseek-reasoner", "deepseek-r1"), 131072, 65536},
	{containsAny("deepseek"), 131072, 8192},
	{containsAny("kimi-k3", "kimi-k2", "moonshot"), 262144, 65536},
	{containsAny("kimi", "moonshot"), 262144, 16384},
	{containsAny("qwen3"), 262144, 65536}, // qwen3 系主流 262K
	{containsAny("qwen2.5", "qwq", "qwen"), 131072, 16384},
	{containsAny("glm-5"), 1048576, 131072}, // GLM 5 系 1M
	{containsAny("glm-4.5", "glm-4.6"), 131072, 65536},
	{containsAny("glm"), 131072, 4096},
	{containsAny("grok-4", "grok-3"), 131072, 16384},
	{containsAny("mistral", "magistral"), 131072, 16384},
	{containsAny("llama-4"), 1048576, 32768},
	{containsAny("llama"), 131072, 8192},
	{containsAny("minimax"), 1000000, 131072},
	{containsAny("doubao-seed-2"), 256000, 128000},
	{containsAny("ernie"), 119000, 64000},
	{containsAny("longcat"), 1000000, 131072},
}

// InferModelCaps 推断模型能力：优先查 models.dev 开源能力库（data/modelsdb.json
// 缓存，后台自动刷新），命中即用库里的真实规格；未命中回落内置有序规则表。
func InferModelCaps(modelID string) config.ModelCaps {
	id := strings.ToLower(strings.TrimSpace(modelID))
	if id == "" {
		return config.ModelCaps{ContextWindow: 128000, MaxTokens: 16384}
	}
	if i := strings.IndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	// 1) 开源库优先（裸 id 与带前缀都查）
	if caps, ok := dbLookupCaps(strings.TrimSpace(modelID)); ok {
		return caps
	}
	// 2) 内置规则表
	caps := config.ModelCaps{ContextWindow: 128000, MaxTokens: 16384}
	for _, r := range capRules {
		if r.match(id) {
			caps.ContextWindow = r.window
			caps.MaxTokens = r.maxTok
			break
		}
	}
	caps.Reasoning = inferReasoning(id)
	caps.Vision = inferVision(id)
	return caps
}

func inferReasoning(id string) bool {
	if containsAny("claude-3-7", "claude-3.7", "claude-sonnet-4", "claude-opus-4", "claude-4")(id) {
		return true
	}
	if oSeriesRe.MatchString(id) || strings.Contains(id, "gpt-5") {
		return true
	}
	if containsAny("deepseek-reasoner", "deepseek-r1", "qwq", "gemini-2.5", "glm-z1", "glm-4.5", "magistral")(id) {
		return true
	}
	return thinkingSuffixRe.MatchString(id)
}

var thinkingSuffixRe = regexp.MustCompile(`-think(ing)?(-|$)`)

func inferVision(id string) bool {
	if containsAny("gpt-4o", "gpt-4-turbo", "gpt-4.1", "gpt-4-vision", "gpt-5", "chatgpt-4o", "o3", "o4",
		"claude", "gemini",
		"qwen-vl", "qwen2-vl", "qwen2.5-vl",
		"glm-4v", "glm-4.5v", "glm-4.6v",
		"grok-2", "grok-3", "grok-4",
		"moonshot-vision", "kimi-latest",
		"pixtral", "llava", "internvl", "minicpm-v", "step-1v", "doubao-vision", "seed-vl")(id) {
		return true
	}
	return vlSuffixRe.MatchString(id)
}

var vlSuffixRe = regexp.MustCompile(`-(vl|vision)(-|$)`)

// ResolveModelCaps 用用户覆盖值合并推断结果。
func ResolveModelCaps(modelID string, o *config.PartialCaps) config.ModelCaps {
	caps := InferModelCaps(modelID)
	if o == nil {
		return caps
	}
	if o.ContextWindow != nil && *o.ContextWindow > 0 {
		caps.ContextWindow = *o.ContextWindow
	}
	if o.MaxTokens != nil && *o.MaxTokens > 0 {
		caps.MaxTokens = *o.MaxTokens
	}
	if o.Reasoning != nil {
		caps.Reasoning = *o.Reasoning
	}
	if o.Vision != nil {
		caps.Vision = *o.Vision
	}
	return caps
}

// ---------------- 显示名与 Base URL ----------------

var nameSplitRe = regexp.MustCompile(`[-_]+`)
var pureDigitsRe = regexp.MustCompile(`^\d+(\.\d+)*$`)

// AutoModelName 由模型 id 生成显示名：
// 按 [-_]+ 切分，`gpt` 与 `o<数字>` 转大写，纯数字版本原样，其余首字母大写。
func AutoModelName(id string) string {
	s := strings.TrimSpace(id)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '/'); i >= 0 && i < len(s)-1 {
		s = s[i+1:]
	}
	parts := nameSplitRe.Split(s, -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		lower := strings.ToLower(p)
		switch {
		case lower == "gpt":
			out = append(out, "GPT")
		case pureDigitsRe.MatchString(p):
			out = append(out, p)
		case oSeriesNameRe.MatchString(lower):
			out = append(out, strings.ToUpper(lower))
		default:
			r := []rune(lower)
			r[0] = []rune(strings.ToUpper(string(r[0])))[0]
			out = append(out, string(r))
		}
	}
	return strings.Join(out, " ")
}

var oSeriesNameRe = regexp.MustCompile(`^o\d+$`)

// NormalizeBaseURL 规范化 Base URL：
// 去空白与尾斜杠；gemini 缺 /v1beta 时补上；anthropic 与 openai 系保留原样。
func NormalizeBaseURL(protocol, base string) string {
	b := strings.TrimSpace(base)
	b = strings.TrimRight(b, "/")
	if b == "" {
		meta, ok := config.MetaOf(protocol)
		if !ok {
			return ""
		}
		b = strings.TrimRight(meta.BaseURL, "/")
	}
	if protocol != config.ProtoGemini {
		return b
	}
	if versionSegmentRe.MatchString(b) {
		return b
	}
	return b + "/v1beta"
}
