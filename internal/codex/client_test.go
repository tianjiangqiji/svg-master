package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// A separate process exercises actual stdio framing, account isolation and logout.
func TestQuotaRPCProcess(t *testing.T) {
	if os.Getenv("SVG_MASTER_QUOTA_TEST_HELPER") != "1" {
		return
	}
	connected := true
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scan.Bytes(), &req) != nil {
			os.Exit(1)
		}
		var result any = map[string]any{}
		switch req.Method {
		case "account/read":
			var account any
			if connected {
				account = map[string]string{"type": "chatgpt", "email": "user@example.com", "planType": "pro"}
			}
			result = map[string]any{"account": account}
		case "account/rateLimits/read":
			result = map[string]any{"rateLimitsByLimitId": map[string]any{"codex": bucket{PlanType: "pro", Primary: &Window{UsedPercent: percent(10), Duration: 300}, Secondary: &Window{UsedPercent: percent(65), Duration: 10080, ResetsAt: 1800000000}}}}
		case "account/logout":
			connected = false
		}
		if req.ID != 0 {
			json.NewEncoder(os.Stdout).Encode(map[string]any{"id": req.ID, "result": result})
		}
	}
	os.Exit(0)
}

func testClient(t *testing.T) *client {
	return testProcessClient(t, "TestQuotaRPCProcess", "SVG_MASTER_QUOTA_TEST_HELPER=1")
}

func testProcessClient(t *testing.T, name, env string) *client {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+name+"$")
	configureCommand(cmd, cmd.Path, cmd.Args[1:])
	cmd.Env = append(os.Environ(), env)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &client{cmd: cmd, in: in, wait: map[int]chan rpcReply{}, done: make(chan struct{}), events: map[string]*subscription{}}
	go c.read(out)
	return c
}

func TestAccountIsolationAndSelectedQuota(t *testing.T) {
	m := New(t.TempDir())
	defer m.Close()
	m.clients["one"] = testClient(t)
	m.clients["two"] = testClient(t)
	ctx := context.Background()
	s := m.Read(ctx, "one")
	if !s.Connected || s.Period != "7day" || s.Window == nil || *s.Window.UsedPercent != 65 {
		t.Fatalf("unexpected quota: %+v", s)
	}
	if status := m.Status(ctx, "one"); !status.Connected || status.Email != s.Email {
		t.Fatal("generation and quota did not share account")
	}
	if err := m.Logout(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if m.Read(ctx, "one").Connected {
		t.Fatal("logout did not disconnect quota account")
	}
	if !m.Read(ctx, "two").Connected {
		t.Fatal("logout affected another provider")
	}
}
