// Package codex connects SVG Master to the official Codex app-server.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"svg-master/internal/netproxy"
)

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type client struct {
	cmd        *exec.Cmd
	in         io.WriteCloser
	mu         sync.Mutex
	writeMu    sync.Mutex
	next       int
	wait       map[int]chan rpcReply
	done       chan struct{}
	loginID    string
	loginError string
	events     map[string]*subscription
}

type subscription struct {
	events chan rpcReply
	done   <-chan struct{}
}

func start(ctx context.Context, dir string) (*client, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return nil, errors.New("使用 Codex 登录需要安装官方 Codex CLI，并将 codex 加入 PATH")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	args := []string{"app-server", "--listen", "stdio://", "-c", "cli_auth_credentials_store=keyring"}
	cmd := exec.Command(path, args...)
	configureCommand(cmd, path, args)
	cmd.Dir = dir
	// A separate Codex home keeps these accounts independent of the user's CLI/app.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch strings.ToUpper(key) {
		case "CODEX_HOME", "OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL":
			continue
		}
		cmd.Env = append(cmd.Env, item)
	}
	cmd.Env = append(cmd.Env, "CODEX_HOME="+filepath.Clean(dir))
	// Codex uses proxy environment variables; inherit the same system-proxy choice as SVG Master.
	for _, scheme := range []string{"http", "https"} {
		req, _ := http.NewRequest("GET", scheme+"://auth.openai.com", nil)
		if p, e := netproxy.Proxy(req); e == nil && p != nil {
			key := strings.ToUpper(scheme) + "_PROXY"
			for i := len(cmd.Env) - 1; i >= 0; i-- {
				k, _, _ := strings.Cut(cmd.Env[i], "=")
				if strings.EqualFold(k, key) {
					cmd.Env = append(cmd.Env[:i], cmd.Env[i+1:]...)
				}
			}
			cmd.Env = append(cmd.Env, key+"="+p.String())
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		stdin.Close()
		return nil, errors.New("无法启动 Codex CLI，请检查安装是否完整")
	}
	c := &client{cmd: cmd, in: stdin, wait: map[int]chan rpcReply{}, done: make(chan struct{}), events: map[string]*subscription{}}
	go c.read(stdout)
	var result json.RawMessage
	if err = c.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "svg_master", "title": "SVG Master", "version": "1.0.0"}, "capabilities": map[string]bool{"experimentalApi": true}}, &result); err != nil {
		c.close()
		return nil, err
	}
	if err = c.write(map[string]any{"method": "initialized"}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

func (c *client) read(r io.Reader) {
	defer close(c.done)
	defer c.cmd.Wait()
	scan := bufio.NewScanner(r)
	scan.Buffer(make([]byte, 4096), 8<<20)
	for scan.Scan() {
		var msg rpcReply
		if json.Unmarshal(scan.Bytes(), &msg) != nil {
			continue
		}
		c.mu.Lock()
		if len(msg.ID) > 0 && msg.Method == "" {
			var id int
			_ = json.Unmarshal(msg.ID, &id)
			if ch := c.wait[id]; ch != nil {
				ch <- msg
			}
		} else if msg.Method == "account/login/completed" {
			var event struct {
				Success bool   `json:"success"`
				LoginID string `json:"loginId"`
			}
			if json.Unmarshal(msg.Params, &event) == nil && event.LoginID == c.loginID {
				c.loginID = ""
				if !event.Success {
					c.loginError = "Codex 登录未完成，请重试"
				} else {
					c.loginError = ""
				}
			}
		}
		var routing struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(msg.Params, &routing)
		sub := c.events[routing.ThreadID]
		c.mu.Unlock()
		if sub != nil {
			select {
			case sub.events <- msg:
			case <-sub.done:
			}
		}
	}
}

func (c *client) write(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return json.NewEncoder(c.in).Encode(v)
}

func (c *client) call(ctx context.Context, method string, params, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan rpcReply, 1)
	c.wait[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.wait, id); c.mu.Unlock() }()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return errors.New("Codex 连接已断开，请重试")
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return fmt.Errorf("Codex 操作 %s 失败（错误码 %d），请检查网络、模型或登录状态", method, msg.Error.Code)
		}
		return json.Unmarshal(msg.Result, result)
	case <-ctx.Done():
		return errors.New("Codex 请求超时，请检查网络后重试")
	case <-c.done:
		return errors.New("Codex 服务已退出，请检查 Codex CLI 版本")
	}
}

func (c *client) close() {
	c.in.Close()
	select {
	case <-c.done:
		return
	case <-time.After(500 * time.Millisecond):
	}
	_ = c.cmd.Process.Kill()
}
