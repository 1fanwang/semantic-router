package testcases

import (
	"context"
	"fmt"

	"k8s.io/client-go/kubernetes"

	pkgtestcases "github.com/vllm-project/semantic-router/e2e/pkg/testcases"
)

func init() {
	pkgtestcases.Register("protocol-codec-anthropic-response-diagnostics", pkgtestcases.TestCase{
		Description: "Anthropic cache diagnostics allow buffered and streamed replies for Messages, Chat, and Responses clients",
		Tags:        []string{"protocol-codec", "anthropic", "response-api", "streaming"},
		Fn:          testAnthropicResponseDiagnostics,
	})
	pkgtestcases.Register("protocol-codec-responses-provider-decorations", pkgtestcases.TestCase{
		Description: "OpenAI Responses provider decorations allow buffered and streamed replies for Responses, Chat, and Messages clients",
		Tags:        []string{"protocol-codec", "response-api", "streaming"},
		Fn:          testResponsesProviderDecorations,
	})
}

func testAnthropicResponseDiagnostics(ctx context.Context, client *kubernetes.Clientset, opts pkgtestcases.TestCaseOptions) error {
	const prompt = "__mock_anthropic_diagnostics__ __mock_protocol_matrix__"
	if err := runProtocolCodecBackendBufferedMatrix(ctx, client, opts, "MoM", "anthropic.messages.v1", prompt, protocolCodecAnthropicReply); err != nil {
		return fmt.Errorf("buffered Anthropic diagnostics: %w", err)
	}
	if err := runProtocolCodecBackendStreamingMatrix(ctx, client, opts, "MoM", "anthropic.messages.v1", prompt, protocolCodecAnthropicReply); err != nil {
		return fmt.Errorf("streamed Anthropic diagnostics: %w", err)
	}
	return nil
}

func testResponsesProviderDecorations(ctx context.Context, client *kubernetes.Clientset, opts pkgtestcases.TestCaseOptions) error {
	const prompt = "__mock_responses_decorations__"
	if err := runProtocolCodecBackendBufferedMatrix(ctx, client, opts, nativeResponsesBackendModel, "openai.responses.v1", prompt, protocolCodecResponsesReply); err != nil {
		return fmt.Errorf("buffered Responses decorations: %w", err)
	}
	if err := runProtocolCodecBackendStreamingMatrix(ctx, client, opts, nativeResponsesBackendModel, "openai.responses.v1", prompt, protocolCodecResponsesReply); err != nil {
		return fmt.Errorf("streamed Responses decorations: %w", err)
	}
	return nil
}
