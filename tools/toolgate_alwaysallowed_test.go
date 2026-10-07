package tools

import "testing"

func TestAlwaysAllowed_ReportProgress(t *testing.T) {
	if err := newAllowGate([]string{"find"})("report_progress"); err != nil {
		t.Fatalf("whitelist hid report_progress: %v", err)
	}
	if !schemaToolNames(t, GetSubagentToolsSchemaJSONFor([]string{"find"}))["report_progress"] {
		t.Fatal("report_progress missing from a restricted schema")
	}
}
