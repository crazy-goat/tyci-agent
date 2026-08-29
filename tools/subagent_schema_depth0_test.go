package tools

import (
	"encoding/json"
	"testing"
)

// TestGetSubagentToolsSchemaJSONForAtDepth_Depth0NeverOffersSubagent is the
// regression test for review finding 5: the function's own doc comment
// asserts "this function only ever runs for a child (depth >= 1)" — true by
// convention today, but nothing in the code enforced it before this fix.
// AllowedDelegationTool(0) returns "subagent", so a caller that got its own
// depth arithmetic wrong (e.g. forgot the "+1" a child's depth is supposed
// to be its caller's own depth plus one) and called this function with 0
// would have the re-add branch silently put "subagent" BACK into a schema
// that subagentDeniedTools/GetSubagentToolsSchema deliberately stripped it
// from a few lines earlier — while the runtime gate
// (subagentToolRunner.Run's unconditional DenySubagentRecursion) would
// still refuse it, reproducing the exact schema-offers/gate-denies mismatch
// this whole file's schema builders exist to prevent. The fix guards the
// re-add branch with "depth >= 1"; this test calls the function directly
// with depth 0 (bypassing every real call site's own depth bookkeeping) and
// asserts "subagent" never appears, whatever allowed is.
func TestGetSubagentToolsSchemaJSONForAtDepth_Depth0NeverOffersSubagent(t *testing.T) {
	for _, allowed := range [][]string{nil, {"read", "find"}} {
		data := GetSubagentToolsSchemaJSONForAtDepth(allowed, 0)
		var entries []map[string]any
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatalf("unmarshal schema for allowed=%v: %v", allowed, err)
		}
		for _, e := range entries {
			fn, ok := e["function"].(map[string]any)
			if !ok {
				continue
			}
			name, _ := fn["name"].(string)
			if name == "subagent" {
				t.Fatalf("allowed=%v: depth 0 must never offer \"subagent\" from this function (subagentDeniedTools already strips it, and the runtime gate refuses it unconditionally for any depth >= 1 caller) — got it back via the re-add branch", allowed)
			}
			if name == "scout" {
				t.Fatalf("allowed=%v: depth 0 must never offer \"scout\" either — AllowedDelegationTool(0) is \"subagent\", not \"scout\", so nothing should have re-added it", allowed)
			}
		}
	}
}
