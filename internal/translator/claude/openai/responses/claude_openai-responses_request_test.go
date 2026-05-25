package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_InputString(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToClaude(
		"claude-opus-4-7[1m]",
		[]byte(`{"input":"Reply exactly: ok","max_tokens":16}`),
		false,
	)

	if got := gjson.GetBytes(out, "model").String(); got != "claude-opus-4-7[1m]" {
		t.Fatalf("model = %q, want claude-opus-4-7[1m]", got)
	}
	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 16 {
		t.Fatalf("max_tokens = %d, want 16", got)
	}
	if got := gjson.GetBytes(out, "messages.#").Int(); got != 1 {
		t.Fatalf("messages count = %d, want 1: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want user", got)
	}
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "Reply exactly: ok" {
		t.Fatalf("messages.0.content = %q, want request input", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_MaxOutputTokensTakesPrecedence(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToClaude(
		"claude-opus-4-7",
		[]byte(`{"input":"hi","max_tokens":16,"max_output_tokens":32}`),
		false,
	)

	if got := gjson.GetBytes(out, "max_tokens").Int(); got != 32 {
		t.Fatalf("max_tokens = %d, want 32", got)
	}
}
