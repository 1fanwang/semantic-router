package protocolcodec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/llmprotocol"
)

// openai/gpt-4.1-nano replies captured on 2026-09-25 from
// https://openrouter.ai/api/v1/chat/completions, stored as the bytes OpenRouter
// returned, including the whitespace it sends ahead of a buffered body.
const (
	openRouterReplyFixture      = "openrouter-chat-out.json"
	openRouterStreamFixture     = "openrouter-chat-stream-out.sse"
	openRouterToolStreamFixture = "openrouter-chat-tool-stream-out.sse"
)

var openRouterClientFormats = []llmprotocol.WireFormat{
	llmprotocol.OpenAIChatV1, llmprotocol.OpenAIResponsesV1, llmprotocol.AnthropicMessagesV1,
}

func openRouterFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "providers", name))
	if err != nil {
		t.Fatalf("no OpenRouter fixture %s: %v", name, err)
	}
	return body
}

func TestOpenRouterReplyDecodesWithItsExtensionsDropped(t *testing.T) {
	response, _, diagnostics, err := NewBuiltinEngine().DecodeResponse(
		llmprotocol.OpenAIChatV1, openRouterFixture(t, openRouterReplyFixture),
	)
	if err != nil {
		t.Fatalf("DecodeResponse() error = %v", err)
	}
	if response.StopReason != llmprotocol.StopEndTurn || len(response.Output) != 1 {
		t.Fatalf("response = %+v", response)
	}
	if response.Usage.Total.Value == nil || *response.Usage.Total.Value != 14 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	assertDiagnosticFields(t, diagnostics, "provider", "choices.native_finish_reason", "usage.cost", "usage.cost_details")
}

func TestOpenRouterReplyTranslatesForEveryClient(t *testing.T) {
	for _, client := range openRouterClientFormats {
		t.Run(string(client), func(t *testing.T) {
			translated, err := NewBuiltinEngine().TranslateResponse(
				llmprotocol.OpenAIChatV1, client, openRouterFixture(t, openRouterReplyFixture), nil,
			)
			if err != nil {
				t.Fatalf("TranslateResponse() error = %v", err)
			}
			if !strings.Contains(string(translated.Body), `"ok"`) {
				t.Fatalf("translated body lost the reply: %s", translated.Body)
			}
		})
	}
}

func TestOpenRouterStreamsTranslateForEveryClient(t *testing.T) {
	tests := []struct {
		fixture string
		want    string
	}{
		{fixture: openRouterStreamFixture, want: `"ok"`},
		{fixture: openRouterToolStreamFixture, want: "Bash"},
	}
	includeUsage := true
	for _, test := range tests {
		for _, client := range openRouterClientFormats {
			t.Run(test.fixture+"/"+string(client), func(t *testing.T) {
				stream, err := NewBuiltinEngine().NewStream(llmprotocol.OpenAIChatV1, client, llmprotocol.StreamContext{
					Context: context.Background(), PublicModel: "or-chat",
					Options: llmprotocol.StreamOptions{IncludeUsage: &includeUsage},
				})
				if err != nil {
					t.Fatal(err)
				}
				frames, _, _, err := stream.Push(openRouterFixture(t, test.fixture))
				if err != nil {
					t.Fatalf("Push() error = %v", err)
				}
				final, _, _, err := stream.Finalize(nil)
				if err != nil {
					t.Fatalf("Finalize() error = %v", err)
				}
				output := string(joinFrames(append(frames, final...)))
				if !strings.Contains(output, test.want) {
					t.Fatalf("translated stream lost %s:\n%s", test.want, output)
				}
			})
		}
	}
}

func TestOpenRouterStreamReportsEachExtensionOnce(t *testing.T) {
	decoder := OpenAIChatCodec{}.NewDecoder(
		llmprotocol.StreamContext{Context: context.Background(), PublicModel: "or-chat"},
		llmprotocol.DefaultPolicy(),
	)
	_, diagnostics, err := decoder.Push(openRouterFixture(t, openRouterToolStreamFixture))
	if err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	counts := map[string]int{}
	for _, diagnostic := range diagnostics {
		counts[diagnostic.Field]++
	}
	if counts["stream.provider"] != 1 || counts["stream.choices.native_finish_reason"] != 1 ||
		counts["stream.usage.cost"] != 1 || counts["stream.usage.cost_details"] != 1 {
		t.Fatalf("diagnostic counts = %v", counts)
	}
}

func TestOpenRouterReplyStillRejectsUnknownFields(t *testing.T) {
	body := strings.Replace(string(openRouterFixture(t, openRouterReplyFixture)), `"provider"`, `"provider_region":"x","provider"`, 1)
	_, _, _, err := NewBuiltinEngine().DecodeResponse(llmprotocol.OpenAIChatV1, []byte(body))
	assertProtocolError(t, err, llmprotocol.ErrorUpstreamUnavailable, "invalid_upstream_json")
}

func joinFrames(frames [][]byte) []byte {
	var joined []byte
	for _, frame := range frames {
		joined = append(joined, frame...)
	}
	return joined
}
