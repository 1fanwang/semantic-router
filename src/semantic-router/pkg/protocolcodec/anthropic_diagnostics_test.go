package protocolcodec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/llmprotocol"
)

// claude-haiku-4-5 replies captured on 2026-09-25 from
// https://api.anthropic.com/v1/messages, stored as the bytes Anthropic
// returned. Both carry the top-level diagnostics field Anthropic now sends.
const (
	anthropicReplyFixture  = "anthropic-messages-out.json"
	anthropicStreamFixture = "anthropic-messages-stream-out.sse"
)

func anthropicFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "providers", name))
	if err != nil {
		t.Fatalf("no Anthropic fixture %s: %v", name, err)
	}
	return body
}

func TestAnthropicReplyWithDiagnosticsTranslatesForEveryClient(t *testing.T) {
	for _, client := range []llmprotocol.WireFormat{
		llmprotocol.AnthropicMessagesV1, llmprotocol.OpenAIChatV1, llmprotocol.OpenAIResponsesV1,
	} {
		t.Run(string(client), func(t *testing.T) {
			_, err := NewBuiltinEngine().TranslateResponse(
				llmprotocol.AnthropicMessagesV1, client, anthropicFixture(t, anthropicReplyFixture), nil,
			)
			if err != nil {
				t.Fatalf("TranslateResponse() error = %v", err)
			}
		})
	}
}

// The captured stream carries signed thinking, so it replays only to Messages clients.
func TestAnthropicStreamWithDiagnosticsReachesMessagesClients(t *testing.T) {
	stream, err := NewBuiltinEngine().NewStream(llmprotocol.AnthropicMessagesV1, llmprotocol.AnthropicMessagesV1,
		llmprotocol.StreamContext{Context: context.Background(), PublicModel: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	frames, _, _, err := stream.Push(anthropicFixture(t, anthropicStreamFixture))
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	final, _, _, err := stream.Finalize(nil)
	if err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	output := ""
	for _, frame := range append(frames, final...) {
		output += string(frame)
	}
	if !strings.Contains(output, `"name":"Bash"`) || !strings.Contains(output, "message_stop") {
		t.Fatalf("translated stream lost the tool call:\n%s", output)
	}
}

func TestAnthropicReplyReportsNonNullDiagnostics(t *testing.T) {
	body := strings.Replace(string(anthropicFixture(t, anthropicReplyFixture)), `"diagnostics":null`,
		`"diagnostics":{"cache_miss_reason":{"type":"system_changed"}}`, 1)
	_, _, diagnostics, err := NewBuiltinEngine().DecodeResponse(llmprotocol.AnthropicMessagesV1, []byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Field == "diagnostics" && diagnostic.Action == llmprotocol.DiagnosticDropped {
			return
		}
	}
	t.Fatalf("no dropped diagnostic for diagnostics: %+v", diagnostics)
}
