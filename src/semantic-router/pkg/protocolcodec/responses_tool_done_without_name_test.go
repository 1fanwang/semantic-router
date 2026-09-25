package protocolcodec

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/llmprotocol"
)

// Forced bash calls streamed on 2026-09-25 by Azure OpenAI (gpt-4o-2024-11-20,
// /openai/v1/responses) and OpenAI (gpt-4o-mini). Neither done event carries
// name; OpenAI dropped it from the event in openai/openai-python@2a98f6a1d.
// The OpenAI lifecycle events keep only declared response fields (#4218).
var responsesToolDoneWithoutNameStreams = []struct {
	fixture string
	vendor  llmprotocol.ResponseVendor
}{
	{fixture: "azure-openai-responses-tool-stream-out.sse", vendor: llmprotocol.ResponseVendorAzure},
	{fixture: "openai-responses-tool-stream-out.sse"},
}

func TestResponsesToolDoneWithoutNameTranslatesForEveryClient(t *testing.T) {
	clients := []llmprotocol.WireFormat{
		llmprotocol.OpenAIChatV1, llmprotocol.OpenAIResponsesV1, llmprotocol.AnthropicMessagesV1,
	}
	for _, source := range responsesToolDoneWithoutNameStreams {
		body, err := os.ReadFile(filepath.Join("testdata", "providers", source.fixture))
		if err != nil {
			t.Fatal(err)
		}
		for _, client := range clients {
			t.Run(source.fixture+"/"+string(client), func(t *testing.T) {
				policy := llmprotocol.DefaultPolicy()
				policy.ResponseVendor = source.vendor
				engine, err := NewEngine(NewBuiltinRegistry(), policy)
				if err != nil {
					t.Fatal(err)
				}
				stream, err := engine.NewStream(llmprotocol.OpenAIResponsesV1, client, llmprotocol.StreamContext{
					Context: context.Background(), PublicModel: "azure-resp",
				})
				if err != nil {
					t.Fatal(err)
				}
				frames, _, _, err := stream.Push(body)
				if err != nil {
					t.Fatalf("Push() error = %v", err)
				}
				final, _, _, err := stream.Finalize(nil)
				if err != nil {
					t.Fatalf("Finalize() error = %v", err)
				}
				output := string(bytes.Join(append(frames, final...), nil))
				if !strings.Contains(output, "bash") || !strings.Contains(output, "echo") {
					t.Fatalf("translated stream lost the bash call:\n%s", output)
				}
			})
		}
	}
}

func TestResponsesToolDoneStillRejectsAChangedName(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "providers", "openai-responses-tool-stream-out.sse"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(body), `"arguments":"{\"command\":\"echo hi\"}","item_id"`,
		`"arguments":"{\"command\":\"echo hi\"}","name":"other","item_id"`, 1)
	decoder := OpenAIResponsesCodec{}.NewDecoder(
		llmprotocol.StreamContext{Context: context.Background(), PublicModel: "azure-resp"},
		llmprotocol.DefaultPolicy(),
	)
	_, _, err = decoder.Push([]byte(changed))
	assertProtocolError(t, err, llmprotocol.ErrorUpstreamUnavailable, "stream_tool_identity_mismatch")
}
