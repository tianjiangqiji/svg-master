package config

import "testing"

func TestDraftPromptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	st := store.Settings()
	st.DraftPrompt = "restore me"
	st.PixelDraftPrompt = "pixel draft"
	if err := store.Save(st); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Settings()
	if got.DraftPrompt != "restore me" || got.PixelDraftPrompt != "pixel draft" {
		t.Fatalf("draft prompts did not persist: %+v", got)
	}
}
