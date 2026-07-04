package display

import "testing"

func TestFormatToolCall_Web(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "full search",
			args: `{"method":"search","what":"golang patterns"}`,
			want: "web(search, golang patterns)",
		},
		{
			name: "full lookup",
			args: `{"method":"lookup","what":"NASA"}`,
			want: "web(lookup, NASA)",
		},
		{
			name: "full get",
			args: `{"method":"get","what":"https://example.com/doc"}`,
			want: "web(get, https://example.com/doc)",
		},
		{
			name: "method only",
			args: `{"method":"search"}`,
			want: "web(search)",
		},
		{
			name: "empty args",
			args: "",
			want: "web(...)",
		},
		{
			name: "invalid json",
			args: "invalid json",
			want: "web(...)",
		},
		{
			name: "long what truncated",
			args: `{"method":"search","what":"a very long string that exceeds sixty characters and should be truncated with an ellipsis at the end"}`,
			want: "web(search, a very long string that exceeds sixty characters and shou...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("web", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"web\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_Skills(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "with name",
			args: `{"name":"go-testing"}`,
			want: "skills(go-testing)",
		},
		{
			name: "empty object",
			args: `{}`,
			want: "skills(list)",
		},
		{
			name: "empty args",
			args: "",
			want: "skills(list)",
		},
		{
			name: "invalid json",
			args: "invalid json",
			want: "skills(list)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("skills", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"skills\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_Find(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "with method and pattern",
			args: `{"method":"grep","pattern":"**/*.go"}`,
			want: `find(grep, **/*.go)`,
		},
		{
			name: "pattern only",
			args: `{"pattern":"main.go"}`,
			want: `find(main.go)`,
		},
		{
			name: "empty args",
			args: "",
			want: "find(...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("find", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"find\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_Todo(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "action and content",
			args: `{"action":"done","content":"Fix login"}`,
			want: "todo(done: Fix login)",
		},
		{
			name: "action only",
			args: `{"action":"list"}`,
			want: "todo(list)",
		},
		{
			name: "empty args",
			args: "",
			want: "todo(...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("todo", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"todo\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_Read(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "with path",
			args: `{"path":"main.go"}`,
			want: "read(main.go)",
		},
		{
			name: "empty args",
			args: "",
			want: "read(...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("read", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"read\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_Write(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "with path",
			args: `{"path":"output.txt"}`,
			want: "write(output.txt)",
		},
		{
			name: "empty args",
			args: "",
			want: "write(...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("write", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"write\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_Bash(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "with description",
			args: `{"description":"Build project"}`,
			want: "bash(Build project)",
		},
		{
			name: "with command",
			args: `{"command":"go build ./..."}`,
			want: "bash(go build ./...)",
		},
		{
			name: "empty args",
			args: "",
			want: "bash(...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("bash", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"bash\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_Subagent(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
	}{
		{
			name: "single task",
			args: `{"task":"Find all Go files"}`,
			want: "subagent(Find all Go files)",
		},
		{
			name: "multiple tasks",
			args: `{"tasks":[{"task":"Build"},{"task":"Test"},{"task":"Lint"}]}`,
			want: "subagent(Build +2)",
		},
		{
			name: "empty args",
			args: "",
			want: "subagent(...)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolCall("subagent", tt.args)
			if got != tt.want {
				t.Errorf("formatToolCall(\"subagent\", %q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestFormatToolCall_UnknownToolFallsBack(t *testing.T) {
	got := formatToolCall("unknown", `{"foo":"bar"}`)
	want := "unknown(...)"
	if got != want {
		t.Errorf("formatToolCall(\"unknown\", ...) = %q, want %q", got, want)
	}
}
