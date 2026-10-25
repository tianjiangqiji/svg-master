package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"svg-master/internal/llm"
)

// ACP 上下文压缩内核：移植 acp-kernel 的语义，纯标准库实现。
//
// 核心不变量：Project 只折叠「投影」，绝不修改 Engine 持有的原始 messages。
// 因此 Decompress 天然可逆——把块的 Active 置回 false，消息就重新出现在投影里。

const (
	// minCompressWindow 小于该窗口时压缩无意义，Compressor 退化为 nil（只保留 nudge）。
	minCompressWindow = 16000
	// emergencyRatio 达到该占比时强制折叠最老消息。
	emergencyRatio = 0.95
	// maxBlockTier 摘要最高层级。
	maxBlockTier = 3
	// pruneAfter 连续多少轮未被引用后视为冗余。
	pruneAfter = 6
	// defaultCompressAt 未配置时的默认触发阈值（百分比）。
	defaultCompressAt = 75
)

// block 一个压缩块（内部表示）。
type block struct {
	BlockID       string
	Tier          int
	Topic         string
	Summary       string
	DirectMsgs    []string // 直接覆盖的消息 ref
	EffectiveMsgs []string // 含子块间接覆盖的消息 ref
	DirectBlocks  []string // 合并来源的子块 ID
	CreatedAt     time.Time
	Survived      int // 存活轮数（Project 次数）
	Generation    int // 创建时所在的代
	Active        bool
	Tokens        int // 摘要自身的 token 估算

	startIdx int // 覆盖区间起点（消息索引，含）
	endIdx   int // 覆盖区间终点（消息索引，含）
}

// state 压缩内核的可变状态。
type state struct {
	blocks  []*block
	msgRefs map[string]int // ref -> 消息索引
	nudge   string         // 最近一次 nudge 文本
	gen     int            // 当前代
}

// compressor Compressor 的默认实现。
type compressor struct {
	mu         sync.Mutex
	window     int // 上下文窗口
	compressAt int // 触发阈值百分比
	st         state
	refs       []string // 消息索引 -> ref
}

// newCompressor 构造压缩内核；窗口过小时返回 nil（调用方需判空）。
func newCompressor(window, compressAt int) Compressor {
	if window < minCompressWindow {
		return nil
	}
	if compressAt <= 0 || compressAt > 100 {
		compressAt = defaultCompressAt
	}
	return &compressor{
		window:     window,
		compressAt: compressAt,
		st: state{
			msgRefs: map[string]int{},
		},
	}
}

// contextWindowOf 取模型上下文窗口；未知时给保守默认值。
func contextWindowOf(model string) int {
	caps := llm.ResolveModelCaps(model, nil)
	if caps.ContextWindow > 0 {
		return caps.ContextWindow
	}
	return 128000
}

// ---- ref 与估算 ----

// refOf 消息索引对应的稳定 ref（m00001 起）。
func refOf(idx int) string { return fmt.Sprintf("m%05d", idx+1) }

// assignRefs 保证前 n 条消息都有 ref（只追加，不改历史）。
func (c *compressor) assignRefs(n int) {
	for len(c.refs) < n {
		r := refOf(len(c.refs))
		c.refs = append(c.refs, r)
		c.st.msgRefs[r] = len(c.refs) - 1
	}
}

// estTokens 粗略估算文本 token：ASCII 4 字符 ≈ 1 token，非 ASCII 1 字符 ≈ 1 token。
func estTokens(s string) int {
	if s == "" {
		return 0
	}
	ascii, other := 0, 0
	for i := 0; i < len(s); {
		r, sz := utf8.DecodeRuneInString(s[i:])
		if r < utf8.RuneSelf {
			ascii++
		} else {
			other++
		}
		i += sz
	}
	return (ascii+3)/4 + other
}

// msgTokens 单条消息的 token 估算。
func msgTokens(m llm.Message) int {
	n := 4
	for _, b := range m.Blocks {
		switch b.Type {
		case llm.BlockText:
			n += estTokens(b.Text)
		case llm.BlockThinking:
			n += estTokens(b.Thinking)
		case llm.BlockToolCall:
			if b.ToolCall != nil {
				n += 6 + estTokens(b.ToolCall.Name) + estTokens(string(b.ToolCall.Arguments))
			}
		case llm.BlockToolResult:
			if b.ToolResult != nil {
				n += 6 + estTokens(b.ToolResult.Content)
			}
		case llm.BlockImage:
			n += 800
		}
	}
	return n
}

// estTokensAll 一组消息的 token 估算。
func estTokensAll(msgs []llm.Message) int {
	t := 0
	for _, m := range msgs {
		t += msgTokens(m)
	}
	return t
}

// ---- Project 管线 ----

// Project 在每次模型请求前调用：返回投影消息与可选 nudge。
func (c *compressor) Project(msgs []llm.Message, tokenCount int) ([]llm.Message, string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.assignRefs(len(msgs))
	c.sync(len(msgs))
	c.merge()
	c.prune()

	pressure := tokenCount
	if pressure <= 0 {
		pressure = estTokensAll(msgs)
	}

	// emergencyTruncate：极端占用时强制折叠最老消息。
	folded := c.emergencyTruncate(len(msgs), pressure)

	active := c.filter()
	hidden := c.hide(active)
	out := c.render(msgs, hidden, active)

	// nudge：以「真实请求大小」为准，压缩过就用投影后的值。
	if folded {
		pressure = estTokensAll(out)
	}
	if c.window > 0 && pressure*100 >= c.window*c.compressAt {
		c.st.nudge = fmt.Sprintf(
			"上下文已用约 %d/%d tokens（%d%%）。请调用 compress 工具折叠已消化的早期消息：ranges=[{startRef:\"%s\", endRef:\"%s\", topic:\"…\", summary:\"…\"}]。保留最近 4～6 条原文。",
			pressure, c.window, pressure*100/c.window, c.firstVisibleRef(msgs, hidden), c.suggestEndRef(msgs, hidden),
		)
	} else {
		c.st.nudge = ""
	}
	c.st.gen++
	for _, b := range active {
		b.Survived++
	}
	return out, c.st.nudge
}

// sync 让块与当前消息规模对齐：越界块失效。
func (c *compressor) sync(n int) {
	for _, b := range c.st.blocks {
		if b.startIdx < 0 || b.endIdx >= n || b.endIdx < b.startIdx {
			b.Active = false
		}
	}
}

// merge 同 topic 且区间相邻的块合并，Tier 升级。
func (c *compressor) merge() {
	for i := 0; i < len(c.st.blocks); i++ {
		a := c.st.blocks[i]
		if !a.Active {
			continue
		}
		for j := i + 1; j < len(c.st.blocks); j++ {
			b := c.st.blocks[j]
			if !b.Active || b.Topic != a.Topic {
				continue
			}
			if b.startIdx != a.endIdx+1 {
				continue
			}
			tier := a.Tier
			if b.Tier > tier {
				tier = b.Tier
			}
			tier++
			if tier > maxBlockTier {
				tier = maxBlockTier
			}
			a.Tier = tier
			a.Summary = "【合并摘要】\n" + a.Summary + "\n" + b.Summary
			a.DirectMsgs = append(a.DirectMsgs, b.DirectMsgs...)
			a.EffectiveMsgs = append(a.EffectiveMsgs, b.EffectiveMsgs...)
			a.DirectBlocks = append(a.DirectBlocks, b.BlockID)
			a.endIdx = b.endIdx
			a.Tokens = estTokens(a.Summary)
			b.Active = false
		}
	}
}

// prune 降级冗余块：被更高层块完整包含的块不再参与投影。
func (c *compressor) prune() {
	for i, a := range c.st.blocks {
		if !a.Active {
			continue
		}
		for j, b := range c.st.blocks {
			if i == j || !b.Active {
				continue
			}
			if b.startIdx <= a.startIdx && b.endIdx >= a.endIdx && (b.Tier > a.Tier || len(b.DirectBlocks) > len(a.DirectBlocks)) {
				a.Active = false
				break
			}
		}
		if a.Active && a.Survived > pruneAfter && len(a.DirectMsgs) == 0 {
			a.Active = false
		}
	}
}

// filter 只保留 Active 块。
func (c *compressor) filter() []*block {
	out := make([]*block, 0, len(c.st.blocks))
	for _, b := range c.st.blocks {
		if b.Active {
			out = append(out, b)
		}
	}
	return out
}

// hide 被块覆盖的消息索引集合。
func (c *compressor) hide(active []*block) map[int]bool {
	h := map[int]bool{}
	for _, b := range active {
		for i := b.startIdx; i <= b.endIdx; i++ {
			h[i] = true
		}
	}
	return h
}

// render 拼装投影：隐藏被覆盖消息，在区间起点插入块摘要，并给每条消息加 ref 标记。
func (c *compressor) render(msgs []llm.Message, hidden map[int]bool, active []*block) []llm.Message {
	starts := map[int]*block{}
	for _, b := range active {
		starts[b.startIdx] = b
	}
	out := make([]llm.Message, 0, len(msgs))
	for i := 0; i < len(msgs); i++ {
		if b, ok := starts[i]; ok {
			out = append(out, blockMessage(b))
		}
		if hidden[i] {
			continue
		}
		ref := refOf(i)
		if i < len(c.refs) {
			ref = c.refs[i]
		}
		out = append(out, decorateMessage(msgs[i], ref))
	}
	return out
}

// firstVisibleRef 第一个未被隐藏的消息 ref（用于 nudge 提示）。
func (c *compressor) firstVisibleRef(msgs []llm.Message, hidden map[int]bool) string {
	for i := range msgs {
		if !hidden[i] {
			return refOf(i)
		}
	}
	return refOf(0)
}

// suggestEndRef 建议折叠的结束 ref：保留最后 5 条原文。
func (c *compressor) suggestEndRef(msgs []llm.Message, hidden map[int]bool) string {
	visible := 0
	for i := range msgs {
		if !hidden[i] {
			visible++
		}
	}
	if visible <= 6 {
		return refOf(len(msgs) - 1)
	}
	want := visible - 5
	seen := 0
	for i := range msgs {
		if hidden[i] {
			continue
		}
		seen++
		if seen == want {
			return refOf(i)
		}
	}
	return refOf(len(msgs) - 1)
}

// blockMessage 块摘要投影成一条消息。
func blockMessage(b *block) llm.Message {
	return llm.TextMsg(llm.RoleUser, fmt.Sprintf(
		"[上下文压缩块 %s · %s · Tier%d · 覆盖 %s～%s]\n%s",
		b.BlockID, b.Topic, b.Tier, refOf(b.startIdx), refOf(b.endIdx), b.Summary))
}

// decorateMessage 给消息加 acp ref 标记（深拷贝，绝不改动原始消息）。
func decorateMessage(m llm.Message, ref string) llm.Message {
	out := cloneMessage(m)
	mark := fmt.Sprintf("<acp tokens=\"~%d\" type=\"text\">%s</acp>", msgTokens(m), ref)
	if m.Role == llm.RoleTool {
		for i := range out.Blocks {
			if out.Blocks[i].Type == llm.BlockToolResult && out.Blocks[i].ToolResult != nil {
				out.Blocks[i].ToolResult.Content = mark + "\n" + stripACPArtifacts(out.Blocks[i].ToolResult.Content)
				return out
			}
		}
	}
	idx := -1
	for i := range out.Blocks {
		if out.Blocks[i].Type == llm.BlockText {
			idx = i
			break
		}
	}
	if idx >= 0 {
		out.Blocks[idx].Text = mark + "\n" + stripACPArtifacts(out.Blocks[idx].Text)
	} else {
		out.Blocks = append([]llm.Block{{Type: llm.BlockText, Text: mark}}, out.Blocks...)
	}
	return out
}

// cloneMessage 深拷贝一条消息（Block 内含指针）。
func cloneMessage(m llm.Message) llm.Message {
	out := llm.Message{Role: m.Role, Blocks: make([]llm.Block, len(m.Blocks))}
	if m.NativeOutput != nil {
		native := *m.NativeOutput
		native.Items = make([]json.RawMessage, len(m.NativeOutput.Items))
		for i, raw := range m.NativeOutput.Items {
			native.Items[i] = append(json.RawMessage(nil), raw...)
		}
		out.NativeOutput = &native
	}
	for i, b := range m.Blocks {
		nb := b
		if b.Image != nil {
			img := *b.Image
			nb.Image = &img
		}
		if b.ToolCall != nil {
			tc := *b.ToolCall
			if b.ToolCall.Arguments != nil {
				tc.Arguments = append([]byte(nil), b.ToolCall.Arguments...)
			}
			nb.ToolCall = &tc
		}
		if b.ToolResult != nil {
			tr := *b.ToolResult
			if len(b.ToolResult.Images) > 0 {
				tr.Images = append([]llm.ImageData(nil), b.ToolResult.Images...)
			}
			nb.ToolResult = &tr
		}
		out.Blocks[i] = nb
	}
	return out
}

// reACPArtifact 匹配模型模仿写出的 acp 标签或裸 ref 标记。
var reACPArtifact = regexp.MustCompile(`(?:<acp\s[^>]*>m\d{1,6}</acp>|\[m\d{1,5}\])`)

// stripACPArtifacts 只剥掉首尾的 acp 标记与空白，中间内容保持原样。
func stripACPArtifacts(s string) string {
	s = strings.TrimSpace(s)
	for {
		loc := reACPArtifact.FindStringIndex(s)
		if loc == nil || loc[0] != 0 {
			break
		}
		s = strings.TrimSpace(s[loc[1]:])
	}
	for {
		s = strings.TrimSpace(s)
		loc := reACPArtifact.FindStringIndex(s)
		if loc == nil || loc[1] != len(s) {
			break
		}
		s = strings.TrimSpace(s[:loc[0]])
	}
	return s
}

// ---- Compress / Decompress / Search / Blocks / Status ----

// Compress 折叠若干区间（摘要由模型提供）。
func (c *compressor) Compress(ranges []CompressRange) (int, int, []string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(ranges) == 0 {
		return 0, 0, nil, errors.New("ranges 不能为空")
	}
	var warnings []string
	created, saved := 0, 0
	for _, r := range ranges {
		start, ok := c.st.msgRefs[strings.TrimSpace(r.StartRef)]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("startRef %q 不存在", r.StartRef))
			continue
		}
		end, ok := c.st.msgRefs[strings.TrimSpace(r.EndRef)]
		if !ok {
			warnings = append(warnings, fmt.Sprintf("endRef %q 不存在", r.EndRef))
			continue
		}
		if start > end {
			start, end = end, start
		}
		if strings.TrimSpace(r.Summary) == "" {
			warnings = append(warnings, fmt.Sprintf("%s～%s 缺少 summary，已跳过", r.StartRef, r.EndRef))
			continue
		}
		if c.overlaps(start, end) {
			warnings = append(warnings, fmt.Sprintf("%s～%s 与已有压缩块重叠，已跳过", r.StartRef, r.EndRef))
			continue
		}
		topic := strings.TrimSpace(r.Topic)
		if topic == "" {
			topic = "未命名"
		}
		refs := make([]string, 0, end-start+1)
		for i := start; i <= end; i++ {
			if i < len(c.refs) {
				refs = append(refs, c.refs[i])
			} else {
				refs = append(refs, refOf(i))
			}
		}
		b := &block{
			BlockID:       fmt.Sprintf("b%03d", len(c.st.blocks)+1),
			Tier:          1,
			Topic:         topic,
			Summary:       strings.TrimSpace(r.Summary),
			DirectMsgs:    refs,
			EffectiveMsgs: append([]string(nil), refs...),
			CreatedAt:     time.Now(),
			Generation:    c.st.gen,
			Active:        true,
			startIdx:      start,
			endIdx:        end,
		}
		b.Tokens = estTokens(b.Summary)
		c.st.blocks = append(c.st.blocks, b)
		created++
		saved += b.Tokens
	}
	return created, saved, warnings, nil
}

// overlaps 判断区间是否与现有 Active 块重叠。
func (c *compressor) overlaps(start, end int) bool {
	for _, b := range c.st.blocks {
		if !b.Active {
			continue
		}
		if start <= b.endIdx && end >= b.startIdx {
			return true
		}
	}
	return false
}

// Decompress 展开一个块，消息重新回到投影。
func (c *compressor) Decompress(blockID string) (int, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	id := strings.TrimSpace(blockID)
	if id == "" {
		return 0, "", errors.New("blockId 不能为空")
	}
	for _, b := range c.st.blocks {
		if b.BlockID != id {
			continue
		}
		if !b.Active {
			return 0, "", fmt.Errorf("压缩块 %s 已经是展开状态", id)
		}
		b.Active = false
		preview := b.Summary
		if len(preview) > 240 {
			preview = preview[:240] + "…"
		}
		return len(b.DirectMsgs), preview, nil
	}
	return 0, "", fmt.Errorf("找不到压缩块 %s", id)
}

// Search 按关键词检索块（topic / summary / blockID）。
func (c *compressor) Search(query string, limit int) []CompressBlock {
	c.mu.Lock()
	defer c.mu.Unlock()

	if limit <= 0 {
		limit = 5
	}
	q := strings.ToLower(strings.TrimSpace(query))
	var hits []CompressBlock
	for _, b := range c.st.blocks {
		if q != "" {
			hay := strings.ToLower(b.Topic + " " + b.Summary + " " + b.BlockID)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		b.Survived = 0
		hits = append(hits, exportBlock(b))
		if len(hits) >= limit {
			break
		}
	}
	return hits
}

// Blocks 返回全部块（含已展开的）。
func (c *compressor) Blocks() []CompressBlock {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]CompressBlock, 0, len(c.st.blocks))
	for _, b := range c.st.blocks {
		out = append(out, exportBlock(b))
	}
	return out
}

// Status 返回用量。
func (c *compressor) Status(tokenCount int) (int, int, int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	used := tokenCount
	blocks, compressed := 0, 0
	for _, b := range c.st.blocks {
		if b.Active {
			blocks++
			compressed += b.endIdx - b.startIdx + 1
		}
	}
	return used, c.window, blocks, compressed
}

// exportBlock 内部块转导出结构。
func exportBlock(b *block) CompressBlock {
	return CompressBlock{
		BlockID:  b.BlockID,
		Tier:     b.Tier,
		Topic:    b.Topic,
		Summary:  b.Summary,
		StartRef: refOf(b.startIdx),
		EndRef:   refOf(b.endIdx),
		Active:   b.Active,
		Tokens:   b.Tokens,
	}
}

// Nudge 返回最近一次 nudge 文本（引擎用于发 EvNudge 事件）。
func (c *compressor) lastNudge() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st.nudge
}

// emergencyTruncate 占比 ≥95% 时把最老的可见消息折叠成占位摘要块。
func (c *compressor) emergencyTruncate(n, pressure int) bool {
	if c.window <= 0 || n == 0 {
		return false
	}
	if float64(pressure) < float64(c.window)*emergencyRatio {
		return false
	}
	hidden := c.hide(c.filter())
	// 找一段连续的可见消息（保留最后 4 条）。
	start := -1
	for i := 0; i < n-4; i++ {
		if !hidden[i] {
			start = i
			break
		}
	}
	if start < 0 {
		return false
	}
	end := start
	for end+1 < n-4 && !hidden[end+1] && end-start < 30 {
		end++
	}
	refs := make([]string, 0, end-start+1)
	for i := start; i <= end; i++ {
		if i < len(c.refs) {
			refs = append(refs, c.refs[i])
		} else {
			refs = append(refs, refOf(i))
		}
	}
	id := fmt.Sprintf("b%03d", len(c.st.blocks)+1)
	b := &block{
		BlockID:       id,
		Tier:          1,
		Topic:         "自动折叠",
		Summary:       fmt.Sprintf("【自动折叠】此处省略 %d 条早期消息（约 %d tokens）。需要回溯细节时调用 decompress(\"%s\")。", len(refs), pressure/n*len(refs), id),
		DirectMsgs:    refs,
		EffectiveMsgs: append([]string(nil), refs...),
		CreatedAt:     time.Now(),
		Generation:    c.st.gen,
		Active:        true,
		startIdx:      start,
		endIdx:        end,
	}
	b.Tokens = estTokens(b.Summary)
	c.st.blocks = append(c.st.blocks, b)
	return true
}
