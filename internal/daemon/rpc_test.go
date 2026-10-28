package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/crazy-goat/tyci-agent/internal/flow"
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

func TestInProcessReadsRunFiles(t *testing.T) {
	home := t.TempDir()
	for _, st := range []*flow.RunState{
		{Version: 1, Run: "1", Workflow: "fix-ci", Repo: "acme/r", Status: "running", Current: "ci"},
		{Version: 1, Run: "2", Workflow: "fix-ci", Repo: "acme/r", Status: "done", Current: "end"},
		{Version: 1, Run: "3", Workflow: "fix-ci", Repo: "acme/other", Status: "running", Current: "ci"},
	} {
		if err := (&flow.Store{Dir: flow.RunDir(home, flow.RepoKey(st.Repo), st.Run)}).Save(st); err != nil {
			t.Fatal(err)
		}
	}
	p := &InProcess{Home: home, Repos: []string{"acme/r"}}
	runs, err := p.RunsList(context.Background())
	if err != nil || len(runs) != 1 || runs[0].Run != "1" || runs[0].Repo != "acme/r" {
		t.Fatalf("RunsList = %+v, %v; want only the open run 1 of acme/r", runs, err)
	}
	got, err := p.RunsGet(context.Background(), "3")
	if err != nil || got.State == nil || got.State.Repo != "acme/other" {
		t.Fatalf("RunsGet(3) = %+v, %v; want the run of any repository", got, err)
	}
	if _, err := p.RunsGet(context.Background(), "9"); err == nil {
		t.Fatal("RunsGet of an unknown run: no error")
	}
	if _, err := p.Status(context.Background()); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("Status error = %v, want ErrNoDaemon", err)
	}
	if runs, _ := (&InProcess{Home: home}).RunsList(context.Background()); runs == nil {
		t.Fatal("RunsList without repos is nil, want an empty list")
	}
}
