package llm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"svg-master/internal/netproxy"
)

const (
	// maxSSELine 单行 SSE 上限（工具参数可能很长）。
	maxSSELine = 8 << 20
	// errBodyLimit 错误信息里保留的响应体字节数上限（再按字符截断到 300）。
	errBodyLimit = 1200
	// errCharLimit 错误信息里响应体的字符数上限。
	errCharLimit = 300
)

// normalizeTransport 补齐零值，避免调用方传空结构时出现 0 超时 / 0 重试。
func normalizeTransport(t Transport) Transport {
	d := DefaultTransport()
	if t.Timeout > 0 {
		d.Timeout = t.Timeout
	}
	if t.IdleTimeout > 0 {
		d.IdleTimeout = t.IdleTimeout
	}
	if t.MaxRetries > 0 {
		d.MaxRetries = t.MaxRetries
	}
	if t.RetryBase > 0 {
		d.RetryBase = t.RetryBase
	}
	if t.MaxRetryDelay > 0 {
		d.MaxRetryDelay = t.MaxRetryDelay
	}
	return d
}

// doer 共享的 HTTP 客户端 + 重试策略。
type doer struct {
	t Transport
	c *http.Client
}

// 所有轮次共用连接池，避免每次工具回传重新建连。
var sharedHTTPTransport = &http.Transport{
	Proxy:                 netproxy.Proxy,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   8,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   30 * time.Second,
	ExpectContinueTimeout: time.Second,
}

func newDoer(t Transport) *doer {
	nt := normalizeTransport(t)
	return &doer{
		t: nt,
		// 不设 http.Client.Timeout：它会把整个流式响应算进总超时。
		// 首字节超时由 do() 里的 AfterFunc 取消实现。
		c: &http.Client{Transport: sharedHTTPTransport},
	}
}

// bodyWithCancel 让请求 ctx 的 cancel 随 body 关闭一起释放，避免 context 泄漏。
type bodyWithCancel struct {
	io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
}

func (b *bodyWithCancel) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.cancel)
	return err
}

// do 执行请求；仅对 408/429/5xx 与网络错误做指数退避重试。
// 非 2xx 响应会原样返回给调用方（由调用方读取响应体生成中文错误）。
func (d *doer) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	attempts := d.t.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("请求已取消：%w", err)
		}
		req, err := build()
		if err != nil {
			return nil, err
		}
		cctx, cancel := context.WithCancel(req.Context())
		req = req.WithContext(cctx)
		// 首字节超时：拿到响应头之前若超时则取消请求；拿到后立即停表，不影响长流。
		timer := time.AfterFunc(d.t.Timeout, cancel)

		resp, err := d.c.Do(req)
		timer.Stop()
		if err != nil {
			cancel()
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("请求已取消：%w", ctxErr)
			}
			lastErr = fmt.Errorf("网络请求失败：%w", err)
			if i == attempts-1 {
				break
			}
			if serr := sleepBackoff(ctx, d.t, i); serr != nil {
				return nil, serr
			}
			continue
		}

		resp.Body = &bodyWithCancel{ReadCloser: resp.Body, cancel: cancel}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		if !retryableStatus(resp.StatusCode) || i == attempts-1 {
			return resp, nil
		}
		// 可重试的状态码：丢弃响应体后重试。
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if serr := sleepBackoff(ctx, d.t, i); serr != nil {
			return nil, serr
		}
	}
	return nil, noRetry(lastErr)
}

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		return true
	}
	return code >= 500 && code <= 599
}

// sleepBackoff 指数退避：RetryBase*2^attempt，上限 MaxRetryDelay，可被 ctx 打断。
func sleepBackoff(ctx context.Context, t Transport, attempt int) error {
	if t.RetryBase <= 0 {
		return nil
	}
	delay := t.RetryBase
	for i := 0; i < attempt; i++ {
		delay *= 2
		if t.MaxRetryDelay > 0 && delay >= t.MaxRetryDelay {
			break
		}
	}
	if t.MaxRetryDelay > 0 && delay > t.MaxRetryDelay {
		delay = t.MaxRetryDelay
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("请求已取消：%w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

// postJSON 发送 JSON POST 请求（每次重试都重建 body）。
func (d *doer) postJSON(ctx context.Context, url string, headers map[string]string, body []byte) (*http.Response, error) {
	return d.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("构造请求失败：%w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		for k, v := range headers {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		return req, nil
	})
}

// getJSON 发送 GET 请求（模型列表用）。
func (d *doer) getJSON(ctx context.Context, url string, headers map[string]string) (*http.Response, error) {
	return d.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("构造请求失败：%w", err)
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
		return req, nil
	})
}

// httpError 生成带状态码与响应体前 300 字符的中文错误。
func httpError(prefix string, resp *http.Response) error {
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
	body := truncateRunes(strings.TrimSpace(string(raw)), errCharLimit)
	if body == "" {
		body = "（响应体为空）"
	}
	status := strings.TrimSpace(resp.Status)
	if status == "" {
		status = http.StatusText(resp.StatusCode)
	}
	// HTTP 层已完成可重试状态码的退避，Agent 不再把同一预算乘一次。
	return noRetry(fmt.Errorf("%s：HTTP %d %s，响应体：%s", prefix, resp.StatusCode, status, body))
}

// truncateRunes 按字符截断，避免切碎 UTF-8。
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------- SSE ----------------

// sseEvent 一帧 SSE（空行分隔）。
type sseEvent struct {
	Event string
	Data  string
}

// sseReader 逐帧读取 SSE；支持 `event:` / `data:` / 注释行 / `[DONE]`。
type sseReader struct {
	sc   *bufio.Scanner
	wd   *idleWatchdog
	done bool
}

func newSSEReader(body io.ReadCloser, idle time.Duration) *sseReader {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSELine)
	return &sseReader{sc: sc, wd: newIdleWatchdog(idle, body)}
}

// Next 返回下一帧；ok=false 表示流结束（含 `[DONE]`）。
func (r *sseReader) Next() (sseEvent, bool, error) {
	var data []string
	var name string
	flush := func() (sseEvent, bool, error) {
		if len(data) == 0 && name == "" {
			return sseEvent{}, false, nil
		}
		out := sseEvent{Event: name, Data: strings.Join(data, "\n")}
		if strings.TrimSpace(out.Data) == "[DONE]" {
			r.done = true
			return sseEvent{}, false, nil
		}
		return out, true, nil
	}
	for r.sc.Scan() {
		r.wd.reset()
		line := strings.TrimSuffix(r.sc.Text(), "\r")
		if line == "" {
			if ev, ok, err := flush(); ok || err != nil || r.done {
				return ev, ok, err
			}
			data = data[:0]
			name = ""
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value := splitSSEField(line)
		switch field {
		case "event":
			name = value
		case "data":
			data = append(data, value)
		}
	}
	if err := r.sc.Err(); err != nil {
		if r.wd.fired() {
			return sseEvent{}, false, fmt.Errorf("流式读取超时：%s 内无数据", r.wd.d)
		}
		return sseEvent{}, false, err
	}
	if ev, ok, err := flush(); ok || err != nil {
		return ev, ok, err
	}
	return sseEvent{}, false, nil
}

func (r *sseReader) stop() { r.wd.stop() }

func splitSSEField(line string) (string, string) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return line, ""
	}
	field := line[:i]
	value := line[i+1:]
	value = strings.TrimPrefix(value, " ")
	return field, value
}

// idleWatchdog 空闲看门狗：超时直接关闭 body，让阻塞的读立即返回。
type idleWatchdog struct {
	d    time.Duration
	body io.Closer

	mu      sync.Mutex
	timer   *time.Timer
	tripped bool
	stopped bool
}

func newIdleWatchdog(d time.Duration, body io.Closer) *idleWatchdog {
	w := &idleWatchdog{d: d, body: body}
	if d > 0 {
		w.timer = time.AfterFunc(d, w.fire)
	}
	return w
}

func (w *idleWatchdog) fire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	w.tripped = true
	w.mu.Unlock()
	if w.body != nil {
		_ = w.body.Close()
	}
}

func (w *idleWatchdog) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer == nil || w.stopped || w.tripped {
		return
	}
	w.timer.Reset(w.d)
}

func (w *idleWatchdog) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
	}
}

// fired 报告看门狗是否因空闲超时而触发过。
func (w *idleWatchdog) fired() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tripped
}

// ---------------- 流泵 ----------------

// pump 把一个 SSE body 泵成 StreamEvent；负责 ctx 取消、看门狗、channel 关闭。
type pump struct {
	ctx        context.Context
	ch         chan<- StreamEvent
	body       io.ReadCloser
	idle       time.Duration
	endCheck   func() error
	terminated bool
}

func newPump(ctx context.Context, ch chan<- StreamEvent, body io.ReadCloser, idle time.Duration) *pump {
	return &pump{ctx: ctx, ch: ch, body: body, idle: idle}
}

// send 发送事件；ctx 已取消时返回 false。
func (p *pump) send(ev StreamEvent) bool {
	select {
	case <-p.ctx.Done():
		return false
	case p.ch <- ev:
		return true
	}
}

// run 读取所有帧并交给 handle；handle 返回 false 表示正常结束。
// 自然结束（含 handle 提前结束）时补发 EvDone；出错时发 EvError。
func (p *pump) run(handle func(sseEvent) (bool, error)) {
	done := make(chan struct{})
	defer close(done)
	defer p.body.Close()
	go func() {
		select {
		case <-p.ctx.Done():
			_ = p.body.Close()
		case <-done:
		}
	}()

	rd := newSSEReader(p.body, p.idle)
	defer rd.stop()

	for {
		ev, ok, err := rd.Next()
		if err != nil {
			if p.ctx.Err() != nil {
				return
			}
			p.send(StreamEvent{Kind: EvError, Err: "读取流式响应失败：" + err.Error(), Cause: err})
			return
		}
		if !ok {
			p.terminated = rd.done
			break
		}
		cont, herr := handle(ev)
		if herr != nil {
			if p.ctx.Err() != nil {
				return
			}
			p.send(StreamEvent{Kind: EvError, Err: herr.Error(), Cause: herr})
			return
		}
		if !cont {
			break
		}
	}
	if p.endCheck != nil {
		if err := p.endCheck(); err != nil {
			p.send(StreamEvent{Kind: EvError, Err: err.Error(), Cause: err})
			return
		}
	}
	p.send(StreamEvent{Kind: EvDone})
}

// sessionHeaders 返回网关前缀缓存亲和的会话头。
func sessionHeaders(sessionID string) map[string]string {
	if sessionID == "" {
		return nil
	}
	return map[string]string{
		"session_id":          sessionID,
		"x-client-request-id": sessionID,
		"x-session-affinity":  sessionID,
	}
}
