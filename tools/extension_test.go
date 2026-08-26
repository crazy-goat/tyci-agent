package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeJobExtensionRequester struct {
	requestID string
	approved  bool
	answered  bool
	resolveOK bool

	gotRequestID  string
	gotSeconds    time.Duration
	gotReason     string
	gotResolveID  string
	gotResolveReq string
	gotApprove    bool
}

func (f *fakeJobExtensionRequester) RequestExtension(id string, seconds time.Duration, reason string) (string, bool) {
	f.gotRequestID = id
	f.gotSeconds = seconds
	f.gotReason = reason
	if f.requestID == "" {
		return "", false
	}
	return f.requestID, true
}

func (f *fakeJobExtensionRequester) WaitExtension(ctx context.Context, id, requestID string) (bool, bool) {
	return f.approved, f.answered
}

func (f *fakeJobExtensionRequester) ResolveExtension(id, requestID string, approve bool) bool {
	f.gotResolveID = id
	f.gotResolveReq = requestID
	f.gotApprove = approve
	return f.resolveOK
}

func withFakeExtensionRequester(t *testing.T, f JobExtensionRequester) {
	t.Helper()
	// Through the getter, not the raw global: jobExtensionRequester is
	// mutex-guarded, so reading it directly here is itself the data race the
	// guard exists to remove — the write side already goes through the
	// setter, which makes a raw read exactly the mismatched pair that
	// -race reports once any setter goroutine is still live.
	old := getJobExtensionRequester()
	SetJobExtensionRequester(f)
	t.Cleanup(func() { SetJobExtensionRequester(old) })
}

func TestRequestTimeoutExtension_NoJob(t *testing.T) {
	fake := &fakeJobExtensionRequester{requestID: "req-1", answered: true, approved: true}
	withFakeExtensionRequester(t, fake)

	res := (&RequestTimeoutExtensionTool{}).Run(context.Background(), map[string]any{
		"seconds": 30,
		"reason":  "finish the test",
	})
	if res.Success {
		t.Fatal("request without a job id should fail")
	}
	if !strings.Contains(res.Error, "no job id") {
		t.Fatalf("expected a clear missing-job error, got %q", res.Error)
	}
	if fake.gotRequestID != "" {
		t.Fatalf("requester should not be called without a job id, got %q", fake.gotRequestID)
	}
}

func TestRequestTimeoutExtension_ValidatesSecondsAndReason(t *testing.T) {
	tool := &RequestTimeoutExtensionTool{}
	for name, input := range map[string]map[string]any{
		"missing seconds":    {"reason": "need more time"},
		"zero seconds":       {"seconds": 0, "reason": "need more time"},
		"negative seconds":   {"seconds": -1, "reason": "need more time"},
		"too many seconds":   {"seconds": 601, "reason": "need more time"},
		"fractional seconds": {"seconds": 1.5, "reason": "need more time"},
		"missing reason":     {"seconds": 30},
		"blank reason":       {"seconds": 30, "reason": "  \t"},
	} {
		t.Run(name, func(t *testing.T) {
			res := tool.Run(context.Background(), input)
			if res.Success {
				t.Fatalf("invalid request reported success: %+v", res)
			}
			if res.Error == "" {
				t.Fatal("invalid request should explain the validation failure")
			}
		})
	}
}

func TestRequestTimeoutExtension_RequestsAndWaits(t *testing.T) {
	fake := &fakeJobExtensionRequester{requestID: "req-42", answered: true, approved: true}
	withFakeExtensionRequester(t, fake)

	ctx := context.WithValue(context.Background(), JobIDCtxKey{}, "job-42")
	res := (&RequestTimeoutExtensionTool{}).Run(ctx, map[string]any{
		"seconds": 45,
		"reason":  "the build is still running",
	})
	if !res.Success {
		t.Fatalf("expected approved request to succeed, got %q", res.Error)
	}
	if fake.gotRequestID != "job-42" || fake.gotSeconds != 45*time.Second || fake.gotReason != "the build is still running" {
		t.Fatalf("requester saw wrong request: id=%q seconds=%s reason=%q", fake.gotRequestID, fake.gotSeconds, fake.gotReason)
	}
}

func extensionSchemaToolNames(schema []map[string]any) map[string]bool {
	names := make(map[string]bool)
	for _, item := range schema {
		fn, ok := item["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if name != "" {
			names[name] = true
		}
	}
	return names
}

func TestRequestTimeoutExtension_SchemaRoleGating(t *testing.T) {
	child := extensionSchemaToolNames(GetSubagentToolsSchema())
	if child["request_timeout_extension"] {
		t.Fatal("child schema must omit request_timeout_extension: subagents have no deadline to extend")
	}
	if !IsSubagentDenied("request_timeout_extension") {
		t.Fatal("request_timeout_extension must be denied to child agents")
	}

	top := extensionSchemaToolNames(GetTopLevelToolsSchema())
	if top["request_timeout_extension"] {
		t.Fatal("top-level schema should omit request_timeout_extension")
	}
	all := extensionSchemaToolNames(GetAllToolsSchema())
	if all["request_timeout_extension"] {
		t.Fatal("full schema must omit request_timeout_extension: no subagent deadline exists")
	}
}

func TestAnswerTool_ExtensionAction(t *testing.T) {
	fake := &fakeJobExtensionRequester{resolveOK: true}
	withFakeExtensionRequester(t, fake)

	res := (&AnswerTool{}).Run(context.Background(), map[string]any{
		"action":     "extension",
		"job_id":     "job-7",
		"request_id": "req-7",
		"approve":    true,
	})
	if !res.Success {
		t.Fatalf("extension answer failed: %q", res.Error)
	}
	if fake.gotResolveID != "job-7" || fake.gotResolveReq != "req-7" || !fake.gotApprove {
		t.Fatalf("resolver saw wrong request: id=%q request=%q approve=%v", fake.gotResolveID, fake.gotResolveReq, fake.gotApprove)
	}

	for name, input := range map[string]map[string]any{
		"missing job id":     {"action": "extension", "request_id": "req-7", "approve": true},
		"missing request id": {"action": "extension", "job_id": "job-7", "approve": true},
		"missing approve":    {"action": "extension", "job_id": "job-7", "request_id": "req-7"},
	} {
		t.Run(name, func(t *testing.T) {
			if result := (&AnswerTool{}).Run(context.Background(), input); result.Success {
				t.Fatalf("invalid extension answer reported success: %+v", result)
			}
		})
	}
}
