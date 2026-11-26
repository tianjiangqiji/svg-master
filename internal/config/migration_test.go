package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidGenModeV5(t *testing.T) {
	for _, mode := range []string{GenModeQuick, GenModeLayers, GenModePixel} {
		if !ValidGenMode(mode) {
			t.Errorf("ValidGenMode(%q) = false，当前模式应合法", mode)
		}
	}
	for _, mode := range []string{"", GenModeAnim, "unknown"} {
		if ValidGenMode(mode) {
			t.Errorf("ValidGenMode(%q) = true，旧值/空值/非法值不应作为当前模式", mode)
		}
	}
}

func TestNormalizeGenModeV5(t *testing.T) {
	cases := []struct {
		name      string
		in        AgentSettings
		mode      string
		gif       bool
		multiStep bool
	}{
		{"旧 anim", AgentSettings{GenMode: GenModeAnim, MultiStep: true}, GenModeQuick, true, false},
		{"quick 保留 GIF", AgentSettings{GenMode: GenModeQuick, GIF: true, MultiStep: true}, GenModeQuick, true, false},
		{"layers 清除 GIF", AgentSettings{GenMode: GenModeLayers, GIF: true}, GenModeLayers, false, true},
		{"pixel 清除 GIF", AgentSettings{GenMode: GenModePixel, GIF: true, MultiStep: true}, GenModePixel, false, false},
		{"非法值回落 layers", AgentSettings{GenMode: "unknown", GIF: true}, GenModeLayers, false, true},
		{"空值按 MultiStep 推断 layers", AgentSettings{MultiStep: true, GIF: true}, GenModeLayers, false, true},
		{"空值按 MultiStep 推断 quick", AgentSettings{GIF: true}, GenModeQuick, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NormalizeGenMode(c.in)
			if got.GenMode != c.mode || got.GIF != c.gif || got.MultiStep != c.multiStep {
				t.Fatalf("归一结果 mode=%q gif=%v multiStep=%v，期望 %q/%v/%v",
					got.GenMode, got.GIF, got.MultiStep, c.mode, c.gif, c.multiStep)
			}
			if again := NormalizeGenMode(got); again != got {
				t.Fatalf("重复归一不幂等：%+v -> %+v", got, again)
			}
		})
	}
}

func TestMigrateTo5(t *testing.T) {
	cases := []struct {
		name      string
		in        Settings
		mode      string
		gif       bool
		multiStep bool
	}{
		{
			name: "v4 anim",
			in:   Settings{Version: 4, Workspace: "svg", Agent: AgentSettings{GenMode: GenModeAnim, MultiStep: true}},
			mode: GenModeQuick, gif: true,
		},
		{
			name: "v4 pixel 工作区",
			in:   Settings{Version: 4, Workspace: "pixel", Agent: AgentSettings{GenMode: GenModeLayers, GIF: true, MultiStep: true}},
			mode: GenModePixel,
		},
		{
			name: "v4 quick",
			in:   Settings{Version: 4, Workspace: "svg", Agent: AgentSettings{GenMode: GenModeQuick, GIF: true, MultiStep: true}},
			mode: GenModeQuick, gif: true,
		},
		{
			name: "v4 layers",
			in:   Settings{Version: 4, Workspace: "svg", Agent: AgentSettings{GenMode: GenModeLayers, GIF: true}},
			mode: GenModeLayers, multiStep: true,
		},
		{
			name: "v4 非法值",
			in:   Settings{Version: 4, Workspace: "svg", Agent: AgentSettings{GenMode: "unknown", GIF: true}},
			mode: GenModeLayers, multiStep: true,
		},
		{
			name: "v5 不再用旧工作区覆盖选择",
			in:   Settings{Version: 5, Workspace: "pixel", Agent: AgentSettings{GenMode: GenModeQuick, GIF: true}},
			mode: GenModeQuick, gif: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MigrateTo5(c.in)
			if got.Version != SettingsVersion {
				t.Fatalf("Version = %d，期望 %d", got.Version, SettingsVersion)
			}
			if got.Agent.GenMode != c.mode || got.Agent.GIF != c.gif || got.Agent.MultiStep != c.multiStep {
				t.Fatalf("迁移结果 mode=%q gif=%v multiStep=%v，期望 %q/%v/%v",
					got.Agent.GenMode, got.Agent.GIF, got.Agent.MultiStep, c.mode, c.gif, c.multiStep)
			}
			if again := MigrateTo5(got); !reflect.DeepEqual(again, got) {
				t.Fatalf("重复迁移不幂等：%+v -> %+v", got, again)
			}
		})
	}
}

func TestMigrateTo4StopsAtVersion4(t *testing.T) {
	v := DefaultSettings()
	v.Version = 3
	v.Agent.GenMode = ""
	v.Agent.MultiStep = false
	v = MigrateTo4(v)
	if v.Version != 4 {
		t.Fatalf("MigrateTo4 后 Version = %d，期望停在 4 以继续执行 v5 迁移", v.Version)
	}
	v = MigrateTo5(v)
	if v.Version != SettingsVersion || v.Agent.GenMode != GenModeQuick {
		t.Fatalf("v3 未完整迁移到 v5：%+v", v)
	}
}

func TestStoreLoadsV4AsV5(t *testing.T) {
	cases := []struct {
		name      string
		workspace string
		mode      string
		gif       bool
		multiStep bool
		wantMode  string
		wantGIF   bool
		wantMulti bool
	}{
		{"anim", "svg", GenModeAnim, false, true, GenModeQuick, true, false},
		{"pixel workspace", "pixel", GenModeLayers, true, true, GenModePixel, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			v := DefaultSettings()
			v.Version = 4
			v.Workspace = c.workspace
			v.Agent.GenMode = c.mode
			v.Agent.GIF = c.gif
			v.Agent.MultiStep = c.multiStep
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			s, err := NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			got := s.Settings()
			if got.Version != SettingsVersion || got.Agent.GenMode != c.wantMode || got.Agent.GIF != c.wantGIF || got.Agent.MultiStep != c.wantMulti {
				t.Fatalf("加载结果 version=%d mode=%q gif=%v multiStep=%v，期望 %d/%q/%v/%v",
					got.Version, got.Agent.GenMode, got.Agent.GIF, got.Agent.MultiStep,
					SettingsVersion, c.wantMode, c.wantGIF, c.wantMulti)
			}
		})
	}
}

func TestGIFJSONKeyAndQuickRoundTrip(t *testing.T) {
	raw, err := json.Marshal(AgentSettings{GenMode: GenModeQuick, GIF: true})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["gif"] != true {
		t.Fatalf("GIF JSON 键错误：%s", raw)
	}

	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := s.Settings()
	st.Agent.GenMode = GenModeQuick
	st.Agent.GIF = true
	st.Agent.MultiStep = true
	if err := s.Save(st); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Settings().Agent; got.GenMode != GenModeQuick || !got.GIF || got.MultiStep {
		t.Fatalf("quick GIF 重启后丢失：%+v", got)
	}
	st = reopened.Settings()
	st.Agent.GenMode = GenModeLayers
	if err := reopened.Save(st); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Settings().Agent; got.GenMode != GenModeLayers || got.GIF || !got.MultiStep {
		t.Fatalf("切换 layers 后未清除 GIF：%+v", got)
	}
}
