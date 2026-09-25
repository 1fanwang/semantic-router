package protocolcodec

import (
	"encoding/json"
	"fmt"

	"github.com/vllm-project/semantic-router/src/semantic-router/pkg/llmprotocol"
)

// OpenAI custom tools take free-form input instead of JSON arguments. Chat
// nests the definition and each call under "custom"; Responses flattens both.
type customToolFormatWire struct {
	Type    string                 `json:"type"`
	Grammar *customToolGrammarWire `json:"grammar,omitempty"`
}

type customToolGrammarWire struct {
	Definition string `json:"definition"`
	Syntax     string `json:"syntax"`
}

type responsesCustomToolFormatWire struct {
	Type       string `json:"type"`
	Definition string `json:"definition,omitempty"`
	Syntax     string `json:"syntax,omitempty"`
}

type chatCustomToolWire struct {
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Format      *customToolFormatWire `json:"format,omitempty"`
}

type chatCustomCallWire struct {
	Name  string `json:"name,omitempty"`
	Input string `json:"input"`
}

func decodeCustomToolFormat(wire *customToolFormatWire) (*llmprotocol.CustomToolFormat, error) {
	if wire == nil {
		return nil, nil
	}
	switch wire.Type {
	case "text":
		if wire.Grammar != nil {
			return nil, invalidCustomToolFormat("custom tool text format cannot carry a grammar")
		}
		return nil, nil
	case "grammar":
		if wire.Grammar == nil {
			return nil, invalidCustomToolFormat("custom tool grammar format requires a grammar")
		}
		return &llmprotocol.CustomToolFormat{Syntax: wire.Grammar.Syntax, Definition: wire.Grammar.Definition}, nil
	default:
		return nil, llmprotocol.NewError(
			llmprotocol.ErrorUnsupportedFeature, "unsupported_tool_format", "custom tool format must be text or grammar", nil,
		)
	}
}

func invalidCustomToolFormat(message string) error {
	return llmprotocol.NewError(llmprotocol.ErrorInvalidRequest, "invalid_tool_format", message, nil)
}

func encodeCustomToolFormat(format *llmprotocol.CustomToolFormat) *customToolFormatWire {
	if format == nil {
		return nil
	}
	return &customToolFormatWire{
		Type:    "grammar",
		Grammar: &customToolGrammarWire{Definition: format.Definition, Syntax: format.Syntax},
	}
}

func decodeChatCustomTool(wire chatToolWire) (llmprotocol.Tool, error) {
	function := wire.Function
	if wire.Custom == nil || function.Name != "" || function.Description != "" || len(function.Parameters) != 0 || function.Strict != nil {
		return llmprotocol.Tool{}, llmprotocol.NewError(
			llmprotocol.ErrorInvalidRequest, "invalid_tool_variant", "Chat custom tools carry only a custom definition", nil,
		)
	}
	format, err := decodeCustomToolFormat(wire.Custom.Format)
	if err != nil {
		return llmprotocol.Tool{}, err
	}
	return llmprotocol.Tool{
		Kind: llmprotocol.ToolKindCustom, Name: wire.Custom.Name, Description: wire.Custom.Description,
		CustomFormat: format, Cache: decodeAnthropicCacheControl(wire.CacheControl),
	}, nil
}

func encodeChatCustomTool(tool llmprotocol.Tool) chatToolWire {
	return chatToolWire{
		Type: "custom",
		Custom: &chatCustomToolWire{
			Name: tool.Name, Description: tool.Description, Format: encodeCustomToolFormat(tool.CustomFormat),
		},
		CacheControl: encodeAnthropicCacheControl(tool.Cache),
	}
}

func decodeChatToolCall(wire chatToolCallWire) (llmprotocol.ToolCall, error) {
	if wire.Type == "custom" {
		if wire.Custom == nil || wire.Function != (chatFunctionCallWire{}) {
			return llmprotocol.ToolCall{}, llmprotocol.NewError(
				llmprotocol.ErrorInvalidRequest, "invalid_tool_call", "Chat custom tool calls carry only a custom call", nil,
			)
		}
		return llmprotocol.ToolCall{
			Kind: llmprotocol.ToolKindCustom, ID: wire.ID, Name: wire.Custom.Name, Arguments: wire.Custom.Input,
		}, nil
	}
	if (wire.Type != "" && wire.Type != "function") || wire.Custom != nil {
		return llmprotocol.ToolCall{}, llmprotocol.NewError(
			llmprotocol.ErrorUnsupportedFeature, "unsupported_tool_call", "only function and custom tool calls enter the model protocol", nil,
		)
	}
	return llmprotocol.ToolCall{ID: wire.ID, Name: wire.Function.Name, Arguments: wire.Function.Arguments}, nil
}

func encodeChatToolCall(call llmprotocol.ToolCall) chatToolCallWire {
	if call.Kind == llmprotocol.ToolKindCustom {
		return chatToolCallWire{ID: call.ID, Type: "custom", Custom: &chatCustomCallWire{Name: call.Name, Input: call.Arguments}}
	}
	return chatToolCallWire{
		ID: call.ID, Type: "function",
		Function: chatFunctionCallWire{Name: call.Name, Arguments: call.Arguments},
	}
}

// A streamed custom call names itself once and then sends input fragments
// under "custom" alone, so the type is only required on the first delta.
func decodeChatToolCallDelta(wire chatChunkToolCallWire) (llmprotocol.ToolCall, error) {
	if wire.Type == "custom" || wire.Custom != nil {
		if wire.Type != "" && wire.Type != "custom" || wire.Custom == nil || wire.Function != (chatFunctionCallWire{}) {
			return llmprotocol.ToolCall{}, invalidProviderResponse("invalid_stream_tool_call", "Chat stream custom tool call delta is invalid")
		}
		return llmprotocol.ToolCall{
			Kind: llmprotocol.ToolKindCustom, ID: wire.ID, Name: wire.Custom.Name, Arguments: wire.Custom.Input,
		}, nil
	}
	if wire.Type != "" && wire.Type != "function" {
		return llmprotocol.ToolCall{}, llmprotocol.NewError(
			llmprotocol.ErrorUnsupportedFeature, "unsupported_tool_call", "only function and custom tool calls enter the model protocol", nil,
		)
	}
	return llmprotocol.ToolCall{ID: wire.ID, Name: wire.Function.Name, Arguments: wire.Function.Arguments}, nil
}

func decodeResponsesCustomTool(body json.RawMessage, request *llmprotocol.Request, policy llmprotocol.Policy) error {
	var tool responsesToolWire
	if err := decodeWireValue(body, &tool, policy); err != nil {
		return err
	}
	if err := rejectUnsupportedRequestFields(map[string]json.RawMessage{
		"tools.allowed_callers": tool.AllowedCallers,
		"tools.defer_loading":   tool.DeferLoading,
	}); err != nil {
		return err
	}
	format, err := decodeResponsesCustomToolFormat(tool.Format)
	if err != nil {
		return err
	}
	request.Tools = append(request.Tools, llmprotocol.Tool{
		Kind: llmprotocol.ToolKindCustom, Name: tool.Name, Description: tool.Description, CustomFormat: format,
	})
	return nil
}

func decodeResponsesCustomToolFormat(wire *responsesCustomToolFormatWire) (*llmprotocol.CustomToolFormat, error) {
	if wire == nil {
		return nil, nil
	}
	nested := &customToolFormatWire{Type: wire.Type}
	if wire.Type == "grammar" || wire.Definition != "" || wire.Syntax != "" {
		nested.Grammar = &customToolGrammarWire{Definition: wire.Definition, Syntax: wire.Syntax}
	}
	return decodeCustomToolFormat(nested)
}

func encodeResponsesCustomTool(tool llmprotocol.Tool) responsesToolWire {
	wire := responsesToolWire{Type: "custom", Name: tool.Name, Description: tool.Description}
	if format := tool.CustomFormat; format != nil {
		wire.Format = &responsesCustomToolFormatWire{Type: "grammar", Definition: format.Definition, Syntax: format.Syntax}
	}
	return wire
}

func decodeResponsesCustomToolCall(item responsesItemWire, index int, policy llmprotocol.Policy) llmprotocol.Message {
	id := item.CallID
	if id == "" {
		id = item.ID
	}
	if id == "" && policy.MissingStableIDs == llmprotocol.MissingIDGenerateStable {
		id = llmprotocol.StableID("responses", fmt.Sprint(index), item.Name, item.Input)
	}
	return llmprotocol.Message{ID: item.ID, Role: llmprotocol.RoleAssistant, Content: []llmprotocol.Content{{
		Kind: llmprotocol.ContentToolCall,
		ToolCall: &llmprotocol.ToolCall{
			Kind: llmprotocol.ToolKindCustom, ID: id, Name: item.Name, Arguments: item.Input,
		},
	}}}
}

func customToolCallIDs(messages []llmprotocol.Message) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, message := range messages {
		for _, content := range message.Content {
			if content.Kind == llmprotocol.ContentToolCall && content.ToolCall != nil &&
				content.ToolCall.Kind == llmprotocol.ToolKindCustom {
				ids[content.ToolCall.ID] = struct{}{}
			}
		}
	}
	return ids
}
