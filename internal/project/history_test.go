package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"svg-master/internal/config"
	"svg-master/internal/llm"
)

func TestNativeHistoryPersistsAcrossReopen(t *testing.T) {
	p, err := Open(t.TempDir(), KindUser)
	if err != nil {
		t.Fatal(err)
	}
	native := &llm.NativeOutput{Protocol: config.ProtoOpenAIResp, Items: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"opaque"}`)}}
	if err := p.SetHistory([]llm.Message{{Role: llm.RoleAssistant, NativeOutput: native}}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(p.Dir(), KindUser)
	if err != nil {
		t.Fatal(err)
	}
	history := reopened.History()
	if len(history) != 1 || history[0].NativeOutput == nil || len(history[0].NativeOutput.Items) != 1 {
		t.Fatalf("原生历史未持久化：%+v", history)
	}
	var before, after any
	_ = json.Unmarshal(native.Items[0], &before)
	_ = json.Unmarshal(history[0].NativeOutput.Items[0], &after)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("原生历史字段发生变化")
	}
}

func TestResetContentClearsProjectAndQuickMode(t *testing.T) {
	for _, kind := range []string{KindQuick, KindUser} {
		t.Run(kind, func(t *testing.T) {
			p, err := Open(t.TempDir(), kind)
			if err != nil {
				t.Fatal(err)
			}
			layer, err := p.AddLayer("旧图层", `<rect width="1" height="1"/>`, 0)
			if err != nil {
				t.Fatal(err)
			}
			locked := true
			if _, err := p.SetLayerProps(layer.ID, LayerProps{Locked: &locked}); err != nil {
				t.Fatal(err)
			}
			if err := p.SetHistory([]llm.Message{llm.TextMsg(llm.RoleUser, "旧内容")}); err != nil {
				t.Fatal(err)
			}
			if err := p.ClearContext(); err != nil {
				t.Fatal(err)
			}
			if err := p.SetHistory([]llm.Message{llm.TextMsg(llm.RoleUser, "新内容")}); err != nil {
				t.Fatal(err)
			}
			canvas := p.Meta().Canvas
			if err := p.ResetContent(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(p.Dir(), kind)
			if err != nil {
				t.Fatal(err)
			}
			if !reopened.IsEmpty() || len(reopened.History()) != 0 || reopened.ContextClearedAt() != 0 || len(reopened.Layers()) != 1 {
				t.Fatalf("清空后仍有内容：%+v", reopened.Meta())
			}
			if reopened.Meta().Canvas != canvas {
				t.Fatal("清空改变了画布规格")
			}
			if _, err := os.Stat(filepath.Join(p.Dir(), "layers", layer.File)); !os.IsNotExist(err) {
				t.Fatalf("旧图层文件仍存在：%v", err)
			}
		})
	}
}
