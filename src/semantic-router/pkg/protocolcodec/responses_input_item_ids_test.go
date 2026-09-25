package protocolcodec

import (
	"encoding/json"
	"testing"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/llmprotocol"
)

// OpenAI validates input item ids by type (msg_, fc_, ...), so an encoder that
// invents one for an id-less item turns a valid request into a 400.
func TestResponsesRequestKeepsOnlySourceItemIDs(t *testing.T) {
	tests := []struct {
		name   string
		source llmprotocol.WireFormat
		body   string
		want   []string
	}{
		{
			name:   "chat tool loop",
			source: llmprotocol.OpenAIChatV1,
			body: `{"model":"m","messages":[{"role":"system","content":"Answer in one word."},
				{"role":"user","content":"Run echo hi"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo hi\"}"}}]},
				{"role":"tool","tool_call_id":"call_1","content":"hi"}],
				"tools":[{"type":"function","function":{"name":"bash","parameters":{"type":"object"}}}]}`,
			want: []string{"", "", "", ""},
		},
		{
			name:   "messages with system prompt",
			source: llmprotocol.AnthropicMessagesV1,
			body: `{"model":"m","max_tokens":64,"system":[{"type":"text","text":"Answer in one word."}],
				"messages":[{"role":"user","content":[{"type":"text","text":"Reply with ok"}]}]}`,
			want: []string{"", ""},
		},
		{
			name:   "responses items with and without ids",
			source: llmprotocol.OpenAIResponsesV1,
			body: `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Run echo hi"}]},
				{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":"hi"},
				{"type":"message","id":"msg_2","role":"user","content":[{"type":"input_text","text":"Thanks"}]}]}`,
			want: []string{"", "fc_1", "", "msg_2"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			translated, err := NewBuiltinEngine().TranslateRequest(
				test.source, llmprotocol.OpenAIResponsesV1, []byte(test.body),
				func(request *llmprotocol.Request) error {
					request.Model = "provider-model"
					return nil
				},
			)
			if err != nil {
				t.Fatalf("TranslateRequest() error = %v", err)
			}
			var wire struct {
				Input []struct {
					ID string `json:"id"`
				} `json:"input"`
			}
			if err := json.Unmarshal(translated.Body, &wire); err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(wire.Input))
			for _, item := range wire.Input {
				got = append(got, item.ID)
			}
			if len(got) != len(test.want) {
				t.Fatalf("input items = %v, want %d items\n%s", got, len(test.want), translated.Body)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("input item ids = %q, want %q\n%s", got, test.want, translated.Body)
				}
			}
		})
	}
}
