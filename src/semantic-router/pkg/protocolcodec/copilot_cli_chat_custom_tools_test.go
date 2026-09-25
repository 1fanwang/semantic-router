package protocolcodec

import (
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/llmprotocol"
)

// The two /v1/chat/completions requests Copilot CLI 1.0.88 sends for one
// apply_patch call when COPILOT_PROVIDER_MODEL_ID names a GPT model. The
// system prompt and descriptions are shortened; IDs are placeholders.
const copilotToolLoopFixture = "testdata/clients/copilot-cli-1.0.88-gpt-tool-loop.json"

type copilotChatBody struct {
	Tools    []json.RawMessage `json:"tools"`
	Messages []struct {
		Role       string            `json:"role"`
		ToolCallID string            `json:"tool_call_id"`
		ToolCalls  []json.RawMessage `json:"tool_calls"`
	} `json:"messages"`
}

type copilotResponsesBody struct {
	Tools []json.RawMessage `json:"tools"`
	Input []struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
		Input  string `json:"input"`
	} `json:"input"`
}

type copilotCustomTool struct {
	Type   string `json:"type"`
	Custom struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Format      struct {
			Grammar struct {
				Syntax     string `json:"syntax"`
				Definition string `json:"definition"`
			} `json:"grammar"`
		} `json:"format"`
	} `json:"custom"`
}

func loadCopilotToolLoop(t *testing.T) []json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(copilotToolLoopFixture)
	if err != nil {
		t.Fatal(err)
	}
	var turns []json.RawMessage
	if err := json.Unmarshal(raw, &turns); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("fixture has %d turns, want 2", len(turns))
	}
	return turns
}

func TestCopilotToolLoopKeepsCustomToolsOnOpenAIBackends(t *testing.T) {
	engine := NewBuiltinEngine()
	for turn, body := range loadCopilotToolLoop(t) {
		request, envelope := decodeCopilotTurn(t, engine, turn, body)
		sent := copilotCustomToolFrom(t, body)

		var chat copilotChatBody
		unmarshalCopilotDispatch(t, engine, llmprotocol.OpenAIChatV1, request, envelope, &chat)
		if !containsJSON(chat.Tools, sent) {
			t.Fatalf("turn %d: Chat dispatch changed or lost the custom tool", turn+1)
		}

		var responses copilotResponsesBody
		unmarshalCopilotDispatch(t, engine, llmprotocol.OpenAIResponsesV1, request, envelope, &responses)
		if !containsJSON(responses.Tools, responsesCustomTool(t, sent)) {
			t.Fatalf("turn %d: Responses dispatch changed or lost the custom tool", turn+1)
		}

		_, err := engine.EncodeRequest(llmprotocol.AnthropicMessagesV1, request, envelope)
		var protocolError *llmprotocol.ProtocolError
		if !errors.As(err, &protocolError) || protocolError.Category != llmprotocol.ErrorUnsupportedFeature ||
			protocolError.Code != "unsupported_capability" {
			t.Fatalf("turn %d to Messages returned %v, want unsupported_capability", turn+1, err)
		}
	}
}

func TestCopilotToolResultTurnKeepsTheCustomCall(t *testing.T) {
	engine := NewBuiltinEngine()
	body := loadCopilotToolLoop(t)[1]
	request, envelope := decodeCopilotTurn(t, engine, 1, body)
	var sent copilotChatBody
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	sentCall := sent.Messages[len(sent.Messages)-2].ToolCalls[0]

	var chat copilotChatBody
	unmarshalCopilotDispatch(t, engine, llmprotocol.OpenAIChatV1, request, envelope, &chat)
	last := len(chat.Messages) - 1
	if call := chat.Messages[last-1]; len(call.ToolCalls) != 1 || !jsonSemanticallyEqual(call.ToolCalls[0], sentCall) {
		t.Fatalf("custom tool call did not reach Chat unchanged: %s", call.ToolCalls)
	}
	if result := chat.Messages[last]; result.Role != "tool" || result.ToolCallID != "call_1" {
		t.Fatalf("tool result did not reach Chat: %+v", result)
	}

	var responses copilotResponsesBody
	unmarshalCopilotDispatch(t, engine, llmprotocol.OpenAIResponsesV1, request, envelope, &responses)
	last = len(responses.Input) - 1
	call, result := responses.Input[last-1], responses.Input[last]
	if call.Type != "custom_tool_call" || call.CallID != "call_1" || call.Name != "apply_patch" || call.Input == "" ||
		result.Type != "custom_tool_call_output" || result.CallID != "call_1" {
		t.Fatalf("custom tool loop did not reach Responses: %+v", responses.Input[last-1:])
	}
}

func decodeCopilotTurn(
	t *testing.T,
	engine *Engine,
	turn int,
	body []byte,
) (llmprotocol.Request, llmprotocol.Envelope) {
	t.Helper()
	request, envelope, _, err := engine.DecodeRequestForMutation(llmprotocol.OpenAIChatV1, body)
	if err != nil {
		t.Fatalf("turn %d: Copilot request rejected: %v", turn+1, err)
	}
	request.Model = "routed-model"
	request.Generation++
	return request, envelope
}

func unmarshalCopilotDispatch(
	t *testing.T,
	engine *Engine,
	format llmprotocol.WireFormat,
	request llmprotocol.Request,
	envelope llmprotocol.Envelope,
	target any,
) {
	t.Helper()
	encoded, err := engine.EncodeRequest(format, request, envelope)
	if err != nil {
		t.Fatalf("encode to %s: %v", format, err)
	}
	if err := json.Unmarshal(encoded.Body, target); err != nil {
		t.Fatal(err)
	}
}

func copilotCustomToolFrom(t *testing.T, body []byte) json.RawMessage {
	t.Helper()
	var sent copilotChatBody
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatal(err)
	}
	for _, tool := range sent.Tools {
		var probe struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(tool, &probe) == nil && probe.Type == "custom" {
			return tool
		}
	}
	t.Fatal("fixture has no custom tool")
	return nil
}

func responsesCustomTool(t *testing.T, chatTool json.RawMessage) json.RawMessage {
	t.Helper()
	var tool copilotCustomTool
	if err := json.Unmarshal(chatTool, &tool); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{
		"type": "custom", "name": tool.Custom.Name, "description": tool.Custom.Description,
		"format": map[string]string{
			"type": "grammar", "syntax": tool.Custom.Format.Grammar.Syntax,
			"definition": tool.Custom.Format.Grammar.Definition,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func containsJSON(values []json.RawMessage, want json.RawMessage) bool {
	for _, value := range values {
		if jsonSemanticallyEqual(value, want) {
			return true
		}
	}
	return false
}
