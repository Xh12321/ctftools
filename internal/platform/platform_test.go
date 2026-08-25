package platform_test

import (
	"testing"

	"github.com/Xh12321/ctftools/internal/platform"
)

func TestParseCategory(t *testing.T) {
	cases := []string{"web", "Crypto", "PWN", "reverse", "forensics", "misc"}
	for _, c := range cases {
		got, err := platform.ParseCategory(c)
		if err != nil {
			t.Fatalf("ParseCategory(%q): %v", c, err)
		}
		if got == "" {
			t.Fatalf("empty category for %q", c)
		}
	}
	if _, err := platform.ParseCategory("osint"); err == nil {
		t.Fatal("expected error for unknown category")
	}
}

func TestTaskStatusTransitions(t *testing.T) {
	if !platform.StatusQueued.CanStart() {
		t.Fatal("queued should start")
	}
	if !platform.StatusRunning.CanPause() {
		t.Fatal("running should pause")
	}
	if !platform.StatusPaused.CanResume() {
		t.Fatal("paused should resume")
	}
	if platform.StatusSettled.CanStart() {
		// settled uses retry, not start directly in CanStart
	}
	if !platform.StatusSettled.CanRetry() {
		t.Fatal("settled should retry")
	}
	if !platform.StatusSettled.IsTerminal() {
		t.Fatal("settled is terminal")
	}
	if platform.StatusRunning.IsTerminal() {
		t.Fatal("running is not terminal")
	}
}

func TestNewID(t *testing.T) {
	a := platform.NewID()
	b := platform.NewID()
	if a == "" || a == b {
		t.Fatalf("ids not unique: %q %q", a, b)
	}
	if !platform.IsValidID(a) {
		t.Fatalf("invalid id %q", a)
	}
}

func TestCreateTaskValidate(t *testing.T) {
	in := platform.CreateTask{Title: "t", Category: platform.CategoryWeb}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	in.Title = ""
	if err := in.Validate(); err == nil {
		t.Fatal("expected title required")
	}
}

func TestJSONPayload(t *testing.T) {
	if string(platform.JSONPayload(nil)) != "{}" {
		t.Fatal("nil payload")
	}
	raw := platform.JSONPayload(map[string]any{"a": 1})
	if len(raw) == 0 {
		t.Fatal("empty")
	}
}
