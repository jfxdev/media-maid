package assist

import (
	"context"
	"net/http"
	"strings"
)

// DefaultAnthropicURL is the Claude API base.
const DefaultAnthropicURL = "https://api.anthropic.com"

// DefaultAnthropicModel is used when LLM_MODEL is empty with LLM_PROVIDER=anthropic.
const DefaultAnthropicModel = "claude-haiku-5-5"

type anthropic struct {
	url, key, model string
	http            *http.Client
	ladder          ladder
}

func newAnthropic(baseURL, key, model string, c *http.Client) *anthropic {
	if baseURL == "" {
		baseURL = DefaultAnthropicURL
	}
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &anthropic{url: strings.TrimRight(baseURL, "/") + "/v1/messages", key: key, model: model, http: c, ladder: ladder{max: 2}}
}

// complete calls the Messages API. No tools are offered and no sampling
// parameters are sent (current models reject temperature and forced
// tool_choice). Levels: 0 JSON-schema structured output at low effort;
// 1 without effort (older models); 2 prompt-only JSON for proxies and models
// without structured outputs.
func (a *anthropic) complete(ctx context.Context, t task, user []byte) ([]byte, error) {
	headers := map[string]string{"x-api-key": a.key, "anthropic-version": "2023-06-01"}
	var out []byte
	err := a.ladder.run(func(level int32) error {
		body := map[string]any{
			"model":      a.model,
			"max_tokens": 4096,
			"system":     t.system,
			"messages":   []map[string]string{{"role": "user", "content": string(user)}},
		}
		cfg := map[string]any{}
		if level == 0 {
			cfg["effort"] = "low"
		}
		if level < 2 {
			cfg["format"] = map[string]any{"type": "json_schema", "schema": t.schema}
		}
		if len(cfg) > 0 {
			body["output_config"] = cfg
		}
		var resp struct {
			StopReason string `json:"stop_reason"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := postJSON(ctx, a.http, a.url, headers, body, &resp); err != nil {
			return err
		}
		// "refusal" and "max_tokens" leave no trustworthy answer: the caller
		// falls back to the deterministic layers.
		if resp.StopReason != "end_turn" {
			return errUnusable
		}
		var text strings.Builder
		for _, b := range resp.Content {
			if b.Type == "text" {
				text.WriteString(b.Text)
			}
		}
		if text.Len() == 0 {
			return errUnusable
		}
		out = []byte(stripFences(text.String()))
		return nil
	})
	return out, err
}
