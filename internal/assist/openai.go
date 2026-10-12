package assist

import (
	"context"
	"net/http"
	"strings"
)

// DefaultOpenAIURL is the OpenAI API base. Any OpenAI-compatible server
// (Ollama, LM Studio, OpenRouter, vLLM) works through LLM_BASE_URL.
const DefaultOpenAIURL = "https://api.openai.com/v1"

type openAI struct {
	url, key, model string
	http            *http.Client
	ladder          ladder
}

func newOpenAI(baseURL, key, model string, c *http.Client) *openAI {
	if baseURL == "" {
		baseURL = DefaultOpenAIURL
	}
	return &openAI{url: strings.TrimRight(baseURL, "/") + "/chat/completions", key: key, model: model, http: c, ladder: ladder{max: 2}}
}

// complete calls /chat/completions. Levels: 0 strict JSON schema with
// temperature 0; 1 without temperature (reasoning models reject it);
// 2 plain JSON mode for servers without schema support.
func (o *openAI) complete(ctx context.Context, t task, user []byte) ([]byte, error) {
	var headers map[string]string
	if o.key != "" {
		headers = map[string]string{"Authorization": "Bearer " + o.key}
	}
	var out []byte
	err := o.ladder.run(func(level int32) error {
		body := map[string]any{
			"model":                 o.model,
			"max_completion_tokens": 2048,
			"messages": []map[string]string{
				{"role": "system", "content": t.system},
				{"role": "user", "content": string(user)},
			},
		}
		if level == 0 {
			body["temperature"] = 0
		}
		if level < 2 {
			body["response_format"] = map[string]any{
				"type":        "json_schema",
				"json_schema": map[string]any{"name": t.name, "strict": true, "schema": t.schema},
			}
		} else {
			body["response_format"] = map[string]any{"type": "json_object"}
		}
		var resp struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content string `json:"content"`
					Refusal string `json:"refusal"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := postJSON(ctx, o.http, o.url, headers, body, &resp); err != nil {
			return err
		}
		if len(resp.Choices) == 0 {
			return errUnusable
		}
		c := resp.Choices[0]
		if c.Message.Refusal != "" || c.FinishReason == "length" || c.FinishReason == "content_filter" || c.Message.Content == "" {
			return errUnusable
		}
		out = []byte(stripFences(c.Message.Content))
		return nil
	})
	return out, err
}
