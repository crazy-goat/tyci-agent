package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"github.com/crazy-goat/tyci-agent/internal/flow"
)

// Backend answers the RPC methods. The socket server uses it, and an
// in-process caller (for example an MCP server, #217) can use it directly.
type Backend interface {
	Status(ctx context.Context) (Status, error)
	RunsList(ctx context.Context) ([]RunInfo, error)
	RunsGet(ctx context.Context, run string) (RunDetail, error)
}

// Status is the answer of the "status" method.
type Status struct {
	PID       int          `json:"pid"`
	StartedAt time.Time    `json:"started_at"`
	Repos     []string     `json:"repos"`
	Workers   int          `json:"workers"`
	Running   []RunRef     `json:"running"`
	Queued    int          `json:"queued"`
	Polls     []PollStatus `json:"polls"`
}

// RunRef is a run that the daemon watches.
type RunRef struct {
	Run     string `json:"run"`
	Repo    string `json:"repo"`
	Trigger string `json:"trigger"`
	Issue   int    `json:"issue,omitempty"`
}

// PollStatus is the last poll of one poller.
type PollStatus struct {
	Name  string    `json:"name"`
	Last  time.Time `json:"last"`
	Error string    `json:"error,omitempty"`
}

// RunInfo is one running or paused run in "runs.list".
type RunInfo struct {
	Run       string    `json:"run"`
	Repo      string    `json:"repo"`
	Workflow  string    `json:"workflow"`
	Issue     int       `json:"issue,omitempty"`
	Status    string    `json:"status"`
	Current   string    `json:"current"`
	Ask       string    `json:"ask,omitempty"`
	PR        int       `json:"pr,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunDetail is the answer of "runs.get": the saved state and its file.
type RunDetail struct {
	State *flow.RunState `json:"state"`
	Path  string         `json:"path"`
}

var _ Backend = (*Daemon)(nil)

// Status implements Backend.
func (d *Daemon) Status(context.Context) (Status, error) {
	s := Status{PID: os.Getpid(), StartedAt: d.started, Workers: d.cfg.Workers, Repos: []string{}, Running: []RunRef{}, Polls: []PollStatus{}}
	for _, r := range d.cfg.Repos {
		s.Repos = append(s.Repos, r.Name)
	}
	d.mu.Lock()
	for id, t := range d.active {
		s.Running = append(s.Running, RunRef{Run: id, Repo: t.Repo, Trigger: t.Kind, Issue: t.Issue})
	}
	s.Queued = d.queued
	for _, p := range d.polls {
		s.Polls = append(s.Polls, p)
	}
	d.mu.Unlock()
	sort.Slice(s.Running, func(i, j int) bool { return s.Running[i].Run < s.Running[j].Run })
	sort.Slice(s.Polls, func(i, j int) bool { return s.Polls[i].Name < s.Polls[j].Name })
	return s, nil
}

// RunsList implements Backend.
func (d *Daemon) RunsList(context.Context) ([]RunInfo, error) {
	out := []RunInfo{}
	for _, r := range d.cfg.Repos {
		out = append(out, ListOpenRuns(d.cfg.Home, r.Name)...)
	}
	return out, nil
}

// RunsGet implements Backend.
func (d *Daemon) RunsGet(_ context.Context, run string) (RunDetail, error) {
	return GetRun(d.cfg.Home, run)
}

// ListOpenRuns returns the running and paused runs of repo, newest first.
func ListOpenRuns(home, repo string) []RunInfo {
	out := []RunInfo{}
	for _, st := range repoRuns(home, repo) {
		if st.Status != "running" && st.Status != "paused" {
			continue
		}
		ri := RunInfo{Run: st.Run, Repo: st.Repo, Workflow: st.Workflow, Issue: st.Issue, Status: st.Status, Current: st.Current, PR: st.PR, UpdatedAt: st.UpdatedAt}
		if st.Status == "paused" && st.Ask != nil {
			ri.Ask = st.Ask.Message
		}
		out = append(out, ri)
	}
	return out
}

// GetRun reads the state of run from the run files.
func GetRun(home, run string) (RunDetail, error) {
	st, path, err := flow.FindRun(home, run)
	if err != nil {
		return RunDetail{}, err
	}
	return RunDetail{State: st, Path: path}, nil
}

// ErrNoDaemon is the Status error of InProcess: no daemon runs.
var ErrNoDaemon = errors.New("no daemon runs: start it with 'tyci daemon'")

// InProcess is a Backend that reads the run files itself. A caller uses it
// when no daemon runs.
type InProcess struct {
	Home string
	// Repos are the repositories ("owner/name") that RunsList lists.
	Repos []string
}

var _ Backend = (*InProcess)(nil)

// Status implements Backend. It always returns ErrNoDaemon.
func (p *InProcess) Status(context.Context) (Status, error) { return Status{}, ErrNoDaemon }

// RunsList implements Backend.
func (p *InProcess) RunsList(context.Context) ([]RunInfo, error) {
	out := []RunInfo{}
	for _, r := range p.Repos {
		out = append(out, ListOpenRuns(p.Home, r)...)
	}
	return out, nil
}

// RunsGet implements Backend.
func (p *InProcess) RunsGet(_ context.Context, run string) (RunDetail, error) {
	return GetRun(p.Home, run)
}

// JSON-RPC 2.0 error codes.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
	codeServer         = -32000
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// maxLine is the longest request line the server reads.
const maxLine = 1 << 20

// Serve answers newline-delimited JSON-RPC 2.0 requests on ln until ln is
// closed. Each connection may send many requests.
func Serve(ln net.Listener, b Backend, logf func(string, ...any)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				logf("socket: %v", err)
			}
			return
		}
		go serveConn(c, b)
	}
}

func serveConn(c net.Conn, b Backend) {
	defer func() { _ = c.Close() }()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	enc := json.NewEncoder(c)
	for sc.Scan() {
		resp, ok := answer(context.Background(), b, sc.Bytes())
		if !ok {
			continue // a notification gets no answer
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func answer(ctx context.Context, b Backend, line []byte) (rpcResponse, bool) {
	resp := rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null")}
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		resp.Error = &rpcError{codeParse, "parse error: " + err.Error()}
		return resp, true
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		resp.Error = &rpcError{codeInvalidRequest, "invalid request: jsonrpc must be \"2.0\" and method must be set"}
		return resp, true
	}
	if len(req.ID) == 0 {
		return resp, false
	}
	resp.ID = req.ID
	var (
		res any
		err error
	)
	switch req.Method {
	case "status":
		res, err = b.Status(ctx)
	case "runs.list":
		res, err = b.RunsList(ctx)
	case "runs.get":
		var p struct {
			Run string `json:"run"`
		}
		if json.Unmarshal(req.Params, &p) != nil || p.Run == "" {
			resp.Error = &rpcError{codeInvalidParams, "runs.get needs params {\"run\": \"<run id>\"}"}
			return resp, true
		}
		res, err = b.RunsGet(ctx, p.Run)
	default:
		resp.Error = &rpcError{codeNoMethod, "method not found: " + req.Method + " (methods: status, runs.list, runs.get)"}
		return resp, true
	}
	if err != nil {
		resp.Error = &rpcError{codeServer, err.Error()}
		return resp, true
	}
	resp.Result = res
	return resp, true
}

// Client calls a daemon over its socket. It implements Backend.
type Client struct {
	Path    string
	Timeout time.Duration // default 5s
}

var (
	_      Backend = (*Client)(nil)
	nextID atomic.Int64
)

// Status implements Backend.
func (c *Client) Status(ctx context.Context) (s Status, err error) {
	return s, c.call(ctx, "status", nil, &s)
}

// RunsList implements Backend.
func (c *Client) RunsList(ctx context.Context) (rs []RunInfo, err error) {
	return rs, c.call(ctx, "runs.list", nil, &rs)
}

// RunsGet implements Backend.
func (c *Client) RunsGet(ctx context.Context, run string) (r RunDetail, err error) {
	return r, c.call(ctx, "runs.get", map[string]string{"run": run}, &r)
}

// RemoteError is an error that the daemon answered.
type RemoteError struct {
	Code    int
	Message string
}

func (e *RemoteError) Error() string { return fmt.Sprintf("daemon: %s (code %d)", e.Message, e.Code) }

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var dl net.Dialer
	conn, err := dl.DialContext(ctx, "unix", c.Path)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if t, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(t)
	}
	id := nextID.Add(1)
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 64<<20)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return err
		}
		return errors.New("daemon closed the connection")
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return &RemoteError{resp.Error.Code, resp.Error.Message}
	}
	return json.Unmarshal(resp.Result, out)
}
