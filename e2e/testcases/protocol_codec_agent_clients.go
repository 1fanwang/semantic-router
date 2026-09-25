package testcases

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/vllm-project/semantic-router/e2e/pkg/fixtures"
	pkgtestcases "github.com/vllm-project/semantic-router/e2e/pkg/testcases"
	"k8s.io/client-go/kubernetes"
)

func init() {
	pkgtestcases.Register("protocol-codec-agent-client-fields", pkgtestcases.TestCase{
		Description: "Codex Responses metadata and Copilot custom tools reach a Chat backend without losing supported fields",
		Tags:        []string{"protocol-codec", "response-api", "agents", "tools"},
		Fn:          testProtocolCodecAgentClientFields,
	})
	pkgtestcases.Register("protocol-codec-azure-ingress", pkgtestcases.TestCase{
		Description: "Azure deployment paths select the model and strip the client API key before Chat dispatch",
		Tags:        []string{"protocol-codec", "azure", "agents", "security"},
		Fn:          testProtocolCodecAzureIngress,
	})
}

func testProtocolCodecAzureIngress(ctx context.Context, client *kubernetes.Clientset, opts pkgtestcases.TestCaseOptions) error {
	session, err := fixtures.OpenServiceSession(ctx, client, opts)
	if err != nil {
		return err
	}
	defer session.Close()
	provider, err := openProtocolCodecProviderSession(ctx, client, opts, "openai.chat.v1")
	if err != nil {
		return err
	}
	defer provider.Close()

	const sessionID = "azure-ingress-codec-e2e"
	path := "/openai/deployments/" + chatBackendModel + "/chat/completions?api-version=2024-10-21"
	result, err := sendProtocolMatrixRaw(ctx, session, path, map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "Azure ingress probe"}},
	}, false, map[string]string{
		"api-key": "azure-client-test-key", "x-vsr-test-session-id": sessionID,
	})
	if err != nil {
		return err
	}
	if result.StatusCode != http.StatusOK {
		return fmt.Errorf("Azure deployment Chat returned HTTP %d: %s", result.StatusCode, truncateString(string(result.Body), 500))
	}
	if err := assertChatCompletionBody(result.Body, `"protocol":"chat_completions"`); err != nil {
		return err
	}
	raw, err := lastProviderSimulatorRequest(ctx, provider, sessionID)
	if err != nil {
		return err
	}
	var observed struct {
		Body          map[string]json.RawMessage `json:"body"`
		APIKeyPresent bool                       `json:"api_key_present"`
	}
	if err := json.Unmarshal(raw, &observed); err != nil {
		return err
	}
	if observed.APIKeyPresent {
		return fmt.Errorf("Azure client api-key reached the provider")
	}
	if len(observed.Body["model"]) == 0 || !strings.Contains(string(raw), "Azure ingress probe") {
		return fmt.Errorf("Azure deployment did not select and dispatch the model: %s", truncateString(string(raw), 500))
	}
	unsupported, err := sendProtocolMatrixRaw(ctx, session,
		"/openai/deployments/"+chatBackendModel+"/embeddings?api-version=2024-10-21",
		map[string]any{"input": "Azure ingress probe"}, false, nil)
	if err != nil {
		return err
	}
	if unsupported.StatusCode != http.StatusNotFound {
		return fmt.Errorf("unsupported Azure deployment operation returned HTTP %d, want 404", unsupported.StatusCode)
	}
	return nil
}

func testProtocolCodecAgentClientFields(ctx context.Context, client *kubernetes.Clientset, opts pkgtestcases.TestCaseOptions) error {
	session, err := fixtures.OpenServiceSession(ctx, client, opts)
	if err != nil {
		return err
	}
	defer session.Close()
	provider, err := openProtocolCodecProviderSession(ctx, client, opts, "openai.chat.v1")
	if err != nil {
		return err
	}
	defer provider.Close()

	const cacheKey = "agent-client-codec-e2e"
	cases := []struct {
		name    string
		marker  string
		path    string
		body    map[string]any
		inspect func(map[string]json.RawMessage) error
	}{
		{
			name: "codex-responses", marker: "Codex agent field probe", path: "/v1/responses",
			body: map[string]any{
				"model": chatBackendModel, "input": "Codex agent field probe", "store": false,
				"include":          []string{"reasoning.encrypted_content"},
				"client_metadata":  map[string]any{"x-codex-turn-metadata": `{"request_kind":"turn"}`},
				"prompt_cache_key": cacheKey,
			},
			inspect: func(body map[string]json.RawMessage) error {
				if string(body["prompt_cache_key"]) != `"`+cacheKey+`"` {
					return fmt.Errorf("Codex cache key was lost in Chat dispatch: %s", body["prompt_cache_key"])
				}
				for _, field := range []string{"include", "client_metadata", "store"} {
					if _, found := body[field]; found {
						return fmt.Errorf("unsupported Codex field %q leaked to Chat provider", field)
					}
				}
				return nil
			},
		},
		{
			name: "copilot-custom-tool", marker: "Copilot custom tool probe", path: "/v1/chat/completions",
			body: map[string]any{
				"model":    chatBackendModel,
				"messages": []map[string]any{{"role": "user", "content": "Copilot custom tool probe"}},
				"tools": []map[string]any{{
					"type": "custom", "custom": map[string]any{
						"name": "bash", "description": "Run a shell command",
						"format": map[string]any{"type": "text"},
					},
				}},
			},
			inspect: func(body map[string]json.RawMessage) error {
				var tools []struct {
					Type   string `json:"type"`
					Custom struct {
						Name string `json:"name"`
					} `json:"custom"`
				}
				if err := json.Unmarshal(body["tools"], &tools); err != nil {
					return fmt.Errorf("decode Copilot provider tools: %w", err)
				}
				if len(tools) != 1 || tools[0].Type != "custom" || tools[0].Custom.Name != "bash" {
					return fmt.Errorf("Copilot custom tool was lost in Chat dispatch: %s", body["tools"])
				}
				return nil
			},
		},
	}

	for _, check := range cases {
		sessionID := "agent-client-" + check.name
		result, err := sendProtocolMatrixRaw(ctx, session, check.path, check.body, false,
			map[string]string{"x-vsr-test-session-id": sessionID})
		if err != nil {
			return fmt.Errorf("%s request: %w", check.name, err)
		}
		if result.StatusCode != http.StatusOK {
			return fmt.Errorf("%s returned HTTP %d: %s", check.name, result.StatusCode, truncateString(string(result.Body), 500))
		}
		if check.path == "/v1/responses" {
			if err := assertResponsesBody(result.Body, `"protocol":"chat_completions"`); err != nil {
				return fmt.Errorf("%s response: %w", check.name, err)
			}
		} else if err := assertChatCompletionBody(result.Body, `"protocol":"chat_completions"`); err != nil {
			return fmt.Errorf("%s response: %w", check.name, err)
		}
		raw, err := lastProviderSimulatorRequest(ctx, provider, sessionID)
		if err != nil {
			return fmt.Errorf("%s provider observation: %w", check.name, err)
		}
		var observation struct {
			Body map[string]json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(raw, &observation); err != nil {
			return fmt.Errorf("%s provider observation decode: %w", check.name, err)
		}
		if len(observation.Body) == 0 || !strings.Contains(string(raw), check.marker) {
			// The unique marker proves this observation belongs to this request.
			return fmt.Errorf("%s provider observation does not match request: %s", check.name, truncateString(string(raw), 500))
		}
		if err := check.inspect(observation.Body); err != nil {
			return err
		}
	}
	return nil
}
