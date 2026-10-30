package tyciconfig

import (
	"encoding/json"
	"testing"
)

func TestModeDefaultsToAuto(t *testing.T) {
	mode, err := Mode("").Resolve()
	if err != nil || mode != ModeAuto {
		t.Fatalf("mode = %q, %v", mode, err)
	}
}

func TestModeRejectsUnknownValue(t *testing.T) {
	for _, text := range []string{`"manual"`, `""`, `null`, `true`, `1`} {
		t.Run(text, func(t *testing.T) {
			var mode Mode
			if err := json.Unmarshal([]byte(text), &mode); err == nil {
				t.Fatalf("accepted %s", text)
			}
		})
	}
	if _, err := Mode("manual").Resolve(); err == nil {
		t.Fatal("accepted programmatic unknown mode")
	}
}

func TestWorkflowModeExplicitValues(t *testing.T) {
	for _, want := range []Mode{ModeAuto, ModeSemi} {
		var cfg Workflow
		if err := json.Unmarshal([]byte(`{"mode":"`+string(want)+`"}`), &cfg); err != nil {
			t.Fatal(err)
		}
		if got, err := cfg.Mode.Resolve(); err != nil || got != want {
			t.Fatalf("mode = %q, %v, want %q", got, err, want)
		}
	}
}
