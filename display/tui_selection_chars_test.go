package display

import "testing"

func TestCutCells(t *testing.T) {
	cases := []struct {
		name string
		text string
		from int
		to   int
		want string
	}{
		{"ascii middle", "abcdef", 1, 4, "bcd"},
		{"from start", "abcdef", 0, 2, "ab"},
		{"past end", "abc", 1, 99, "bc"},
		{"wide char full", "a界b", 1, 3, "界"},
		{"wide char before", "a界b", 0, 1, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cutCells(tc.text, tc.from, tc.to); got != tc.want {
				t.Fatalf("cutCells(%q,%d,%d)=%q want %q", tc.text, tc.from, tc.to, got, tc.want)
			}
		})
	}
}
