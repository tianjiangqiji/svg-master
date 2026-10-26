package llm

import (
	"encoding/json"
	"testing"
)

func TestResponsesFastAndActualServiceTier(t *testing.T) {
	c := &openaiResponses{}
	b, err := c.buildBody(Request{Model: "test", Stream: true, ServiceTier: "priority", Messages: []Message{TextMsg(RoleUser, "test")}})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.Unmarshal(b, &body)
	if body["service_tier"] != "priority" || body["stream"] != true || body["store"] != false {
		t.Fatal("Fast changed streaming or storage settings")
	}
	for _, tier := range []string{"priority", "default", ""} {
		raw, _ := json.Marshal(map[string]any{"status": "completed", "service_tier": tier, "output": []any{}})
		found := false
		for _, e := range c.decodeNonStream(raw) {
			if e.Kind == EvServiceTier {
				found = true
				if e.Text != tier {
					t.Fatal("reported requested tier instead of actual tier")
				}
			}
		}
		if !found {
			t.Fatal("missing service tier event")
		}
	}
}
