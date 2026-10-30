package tyciconfig

import (
	"encoding/json"
	"fmt"
)

// Mode selects whether a workflow needs human review after the LLM review.
// Its zero value is an omitted setting, which resolves to auto.
type Mode string

const (
	ModeAuto Mode = "auto"
	ModeSemi Mode = "semi"
)

// Resolve applies the default and rejects unknown modes.
func (m Mode) Resolve() (Mode, error) {
	switch m {
	case "", ModeAuto:
		return ModeAuto, nil
	case ModeSemi:
		return ModeSemi, nil
	default:
		return "", fmt.Errorf("mode must be auto or semi (got %q)", m)
	}
}

// UnmarshalJSON validates explicit settings without losing an omitted mode.
func (m *Mode) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("mode must be auto or semi: %w", err)
	}
	if value != string(ModeAuto) && value != string(ModeSemi) {
		return fmt.Errorf("mode must be auto or semi (got %s)", data)
	}
	*m = Mode(value)
	return nil
}

// Workflow holds one workflows.<name> override in config.json. An omitted mode
// inherits the mode of the workflow definition or the user config.
type Workflow struct {
	Mode Mode `json:"mode,omitempty"`
}
