package config

import "testing"

func TestFastModePatchPersistence(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Settings().FastMode {
		t.Fatal("Fast must default off")
	}
	enabled := true
	patch := SettingsPatch{FastMode: &enabled}
	if patch.Empty() {
		t.Fatal("Fast patch ignored")
	}
	if err = s.Save(patch.Apply(s.Settings())); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Settings().FastMode {
		t.Fatal("Fast setting lost on restart")
	}
	if !((SettingsPatch{}).Apply(reopened.Settings())).FastMode {
		t.Fatal("unrelated settings patch cleared Fast")
	}
	disabled := false
	if (SettingsPatch{FastMode: &disabled}).Apply(reopened.Settings()).FastMode {
		t.Fatal("Fast cannot be turned off")
	}
}
