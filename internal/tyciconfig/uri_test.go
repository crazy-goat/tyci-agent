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

// TestProviderURIFallbacksOptOptionRoundTrip covers ?fallbacks=false combined
// with the existing api=responses and reasoning= options: none of the three
// keys should be dropped or duplicated, and String() must re-emit them in the
// same stable order Parse() read them in.
func TestProviderURIFallbacksOptOptionRoundTrip(t *testing.T) {
	want := "openai://gpt-5.6-luna@$KEY@example.com/v1?api=responses&reasoning=xhigh&fallbacks=false"
	u, err := Parse(want)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if u.Protocol != "responses" {
		t.Fatalf("Protocol = %q, want responses", u.Protocol)
	}
	if u.ReasoningEffort != "xhigh" {
		t.Fatalf("ReasoningEffort = %q, want xhigh", u.ReasoningEffort)
	}
	if u.Fallbacks != "false" {
		t.Fatalf("Fallbacks = %q, want false", u.Fallbacks)
	}
	if got := u.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

// TestProviderURIFallbacksAlone covers ?fallbacks=false with no other query
// option present, and confirms an absent option round-trips back to "".
func TestProviderURIFallbacksAlone(t *testing.T) {
	want := "openai://gpt-5.6-luna@$KEY@example.com/v1?fallbacks=false"
	u, err := Parse(want)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if u.Fallbacks != "false" {
		t.Fatalf("Fallbacks = %q, want false", u.Fallbacks)
	}
	if got := u.String(); got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}

	noFallbacks, err := Parse("openai://gpt-5.6-luna@$KEY@example.com/v1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if noFallbacks.Fallbacks != "" {
		t.Fatalf("Fallbacks = %q, want empty when option is absent", noFallbacks.Fallbacks)
	}
}
