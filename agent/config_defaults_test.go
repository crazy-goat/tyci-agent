package agent

import "testing"

func TestDefaultsWatchConfigIsGlobalOnly(t *testing.T) {
	on := TyciConfig{Defaults: &DefaultsConfig{WatchConfig: true}}
	if !mergeTyciConfig(on, TyciConfig{}).WatchConfigEnabled() {
		t.Error("global watch_config must be kept")
	}
	if mergeTyciConfig(TyciConfig{}, on).WatchConfigEnabled() {
		t.Error("project-local watch_config must be ignored")
	}
	if (TyciConfig{}).WatchConfigEnabled() {
		t.Error("absent defaults must mean off")
	}
}
