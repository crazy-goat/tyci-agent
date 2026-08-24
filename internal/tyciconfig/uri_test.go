package tyciconfig

import "testing"

func TestProviderURIResponsesOptionRoundTrip(t *testing.T) {
	want := "openai://gpt-5.6-luna@$KEY@example.com/v1?api=responses&reasoning=xhigh"
	u, err := Parse(want)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if u.APIType != "openai" {
		t.Fatalf("APIType = %q, want openai", u.APIType)
	}
	if u.Protocol != "responses" {
		t.Fatalf("Protocol = %q, want responses", u.Protocol)
	}
	if u.Reasoning {
		t.Fatal("Reasoning = true, want false for an effort value")
	}
	if u.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want xhigh", u.ReasoningEffort)
	}
	if got := u.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
