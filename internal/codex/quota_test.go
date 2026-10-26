package codex

import "testing"

func percent(n float64) *float64 { return &n }

func TestSelectWindowFollowsPlanAndDuration(t *testing.T) {
	five := &Window{UsedPercent: percent(25), Duration: 300, ResetsAt: 100}
	week := &Window{UsedPercent: percent(40), Duration: 10080, ResetsAt: 200}
	for _, test := range []struct {
		name, plan, label string
		b                 bucket
		want              *Window
	}{
		{"plus", "plus", "5h", bucket{Primary: five, Secondary: week}, five},
		{"pro", "pro", "7day", bucket{Primary: five, Secondary: week}, week},
		{"pro-variant", "pro_200", "7day", bucket{Primary: five, Secondary: week}, week},
		{"pro-lite", "prolite", "7day", bucket{Primary: five, Secondary: week}, week},
		{"swapped", "pro", "7day", bucket{Primary: week, Secondary: five}, week},
		{"missing-week", "pro", "7day", bucket{Primary: five}, nil},
		{"unknown-plan", "team", "", bucket{Primary: five, Secondary: week}, nil},
		{"missing-percent", "plus", "5h", bucket{Primary: &Window{Duration: 300}}, nil},
		{"invalid-percent", "plus", "5h", bucket{Primary: &Window{Duration: 300, UsedPercent: percent(101)}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			label, w := selectWindow(test.plan, test.b)
			if label != test.label || w != test.want {
				t.Fatalf("got %q/%v, want %q/%v", label, w, test.label, test.want)
			}
		})
	}
}

func TestZeroUsageIsAvailable(t *testing.T) {
	w := &Window{Duration: 300, UsedPercent: percent(0)}
	_, got := selectWindow("plus", bucket{Primary: w})
	if got == nil {
		t.Fatal("zero usage must not be confused with unavailable usage")
	}
}
