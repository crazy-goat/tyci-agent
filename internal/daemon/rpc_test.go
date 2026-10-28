package daemon

import (
	"context"
	"errors"
	"testing"
)

type errBackend struct{}

func (errBackend) Status(context.Context) (Status, error)      { return Status{Workers: 2}, nil }
func (errBackend) RunsList(context.Context) ([]RunInfo, error) { return nil, errors.New("disk gone") }
func (errBackend) RunsGet(context.Context, string) (RunDetail, error) {
	return RunDetail{}, nil
}

func TestRPCAnswerErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		code       int
	}{
		{"parse", `{not json`, codeParse},
		{"no version", `{"id":1,"method":"status"}`, codeInvalidRequest},
		{"no method", `{"jsonrpc":"2.0","id":1}`, codeInvalidRequest},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"runs.kill"}`, codeNoMethod},
		{"runs.get without run", `{"jsonrpc":"2.0","id":1,"method":"runs.get","params":{}}`, codeInvalidParams},
		{"backend error", `{"jsonrpc":"2.0","id":1,"method":"runs.list"}`, codeServer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, ok := answer(context.Background(), errBackend{}, []byte(tc.line))
			if !ok || resp.Error == nil || resp.Error.Code != tc.code {
				t.Fatalf("answer %+v (ok %v), want code %d", resp, ok, tc.code)
			}
		})
	}
}

func TestRPCAnswerKeepsIDAndSkipsNotifications(t *testing.T) {
	resp, ok := answer(context.Background(), errBackend{}, []byte(`{"jsonrpc":"2.0","id":"a7","method":"status"}`))
	if !ok || resp.Error != nil || string(resp.ID) != `"a7"` || resp.Result.(Status).Workers != 2 {
		t.Fatalf("answer %+v (ok %v)", resp, ok)
	}
	if _, ok := answer(context.Background(), errBackend{}, []byte(`{"jsonrpc":"2.0","method":"status"}`)); ok {
		t.Fatal("a notification (no id) got an answer")
	}
}
