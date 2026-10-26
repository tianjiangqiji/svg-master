package codex

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Window struct {
	UsedPercent *float64 `json:"usedPercent"`
	Duration    int      `json:"windowDurationMins"`
	ResetsAt    int64    `json:"resetsAt"`
}
type Snapshot struct {
	Connected bool    `json:"connected"`
	Pending   bool    `json:"pending"`
	PlanType  string  `json:"planType,omitempty"`
	Email     string  `json:"email,omitempty"`
	Window    *Window `json:"window,omitempty"`
	Period    string  `json:"period,omitempty"`
	UpdatedAt int64   `json:"updatedAt,omitempty"`
	Message   string  `json:"message,omitempty"`
}
type bucket struct {
	PlanType  string  `json:"planType"`
	Primary   *Window `json:"primary"`
	Secondary *Window `json:"secondary"`
}
type limits struct {
	RateLimits bucket            `json:"rateLimits"`
	ByID       map[string]bucket `json:"rateLimitsByLimitId"`
}
type Manager struct {
	mu      sync.Mutex
	dir     string
	clients map[string]*client
}

func New(dir string) *Manager {
	// Keep this directory stable: Codex's keyring entry is tied to its home path.
	// It also reuses accounts created by the earlier quota-only integration.
	return &Manager{dir: filepath.Join(dir, "codex-quota"), clients: map[string]*client{}}
}
func (m *Manager) get(ctx context.Context, id string) (*client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.clients[id]; c != nil {
		select {
		case <-c.done:
			delete(m.clients, id)
		default:
			return c, nil
		}
	}
	dir := filepath.Join(m.dir, fmt.Sprintf("%x", sha256.Sum256([]byte(id))))
	c, err := start(ctx, dir)
	if err == nil {
		m.clients[id] = c
	}
	return c, err
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.close()
	}
	m.clients = map[string]*client{}
}

func (m *Manager) Status(ctx context.Context, id string) Snapshot {
	c, err := m.get(ctx, id)
	if err != nil {
		return Snapshot{Message: err.Error()}
	}
	c.mu.Lock()
	s := Snapshot{Pending: c.loginID != "", Message: c.loginError}
	c.mu.Unlock()
	if s.Pending {
		return s
	}
	var account struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err = c.call(ctx, "account/read", map[string]bool{"refreshToken": false}, &account); err != nil {
		s.Message = err.Error()
		return s
	}
	if account.Account == nil || account.Account.Type != "chatgpt" {
		if s.Message == "" {
			s.Message = "请先登录 Codex，生成和额度查询共用此账号"
		}
		return s
	}
	s.Connected = true
	s.Email = account.Account.Email
	s.PlanType = account.Account.PlanType
	s.Message = ""
	return s
}

func (m *Manager) Read(ctx context.Context, id string) Snapshot {
	s := m.Status(ctx, id)
	if !s.Connected {
		return s
	}
	c, err := m.get(ctx, id)
	if err != nil {
		s.Message = err.Error()
		return s
	}
	var data limits
	if err = c.call(ctx, "account/rateLimits/read", map[string]any{}, &data); err != nil {
		s.Message = err.Error()
		return s
	}
	b := data.RateLimits
	if v, ok := data.ByID["codex"]; ok {
		b = v
	}
	if b.PlanType != "" {
		s.PlanType = b.PlanType
	}
	s.Period, s.Window = selectWindow(s.PlanType, b)
	s.UpdatedAt = time.Now().Unix()
	s.Message = ""
	if s.Period == "" {
		s.Message = "当前仅显示 Plus（5h）与 Pro（7day）额度"
	} else if s.Window == nil {
		s.Message = "服务端暂未返回 " + s.Period + " 额度"
	}
	return s
}

func selectWindow(plan string, b bucket) (string, *Window) {
	var label string
	var mins int
	switch {
	case strings.EqualFold(plan, "plus"):
		label = "5h"
		mins = 300
	case strings.EqualFold(plan, "pro") || strings.EqualFold(plan, "prolite") || strings.HasPrefix(strings.ToLower(plan), "pro_"):
		label = "7day"
		mins = 10080
	default:
		return "", nil
	}
	for _, w := range []*Window{b.Primary, b.Secondary} {
		if w != nil && w.Duration == mins && w.UsedPercent != nil && *w.UsedPercent >= 0 && *w.UsedPercent <= 100 {
			return label, w
		}
	}
	return label, nil
}

func (m *Manager) Login(ctx context.Context, id string) (string, error) {
	c, err := m.get(ctx, id)
	if err != nil {
		return "", err
	}
	if err = m.Cancel(ctx, id); err != nil {
		return "", err
	}
	var result struct {
		LoginID string `json:"loginId"`
		AuthURL string `json:"authUrl"`
	}
	if err = c.call(ctx, "account/login/start", map[string]any{"type": "chatgpt"}, &result); err != nil {
		return "", err
	}
	u, err := url.Parse(result.AuthURL)
	if err != nil || u.Scheme != "https" || (u.Host != "auth.openai.com" && u.Host != "chatgpt.com") {
		return "", fmt.Errorf("Codex 返回了无效的登录地址")
	}
	c.mu.Lock()
	c.loginID = result.LoginID
	c.loginError = ""
	c.mu.Unlock()
	return result.AuthURL, nil
}
func (m *Manager) Cancel(ctx context.Context, id string) error {
	c, err := m.get(ctx, id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	loginID := c.loginID
	c.mu.Unlock()
	if loginID == "" {
		return nil
	}
	var result any
	if err = c.call(ctx, "account/login/cancel", map[string]string{"loginId": loginID}, &result); err != nil {
		return err
	}
	c.mu.Lock()
	c.loginID = ""
	c.loginError = ""
	c.mu.Unlock()
	return nil
}
func (m *Manager) Logout(ctx context.Context, id string) error {
	if err := m.Cancel(ctx, id); err != nil {
		return err
	}
	c, err := m.get(ctx, id)
	if err != nil {
		return err
	}
	var result any
	if err = c.call(ctx, "account/logout", map[string]any{}, &result); err != nil {
		return err
	}
	c.mu.Lock()
	c.loginError = ""
	c.mu.Unlock()
	return nil
}
