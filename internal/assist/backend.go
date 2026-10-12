package assist

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
)

// maxResponse bounds every response body read from the model endpoint.
const maxResponse = 64 << 10

// backend sends one single-turn request and returns the model's JSON answer.
type backend interface {
	complete(ctx context.Context, t task, user []byte) ([]byte, error)
}

// statusError is a non-2xx reply. The body is never kept: it may echo the
// request or credentials.
type statusError struct{ code int }

func (e statusError) Error() string { return fmt.Sprintf("llm status %d", e.code) }

// errUnusable means the endpoint answered but gave nothing we can use
// (refusal, truncated output, empty content).
var errUnusable = errors.New("llm response unusable")

// postJSON sends body and decodes the reply into out. Errors never contain
// the request, the response body or the headers.
func postJSON(ctx context.Context, c *http.Client, url string, headers map[string]string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return errors.New("llm request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("llm request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponse))
		return statusError{resp.StatusCode}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(out); err != nil {
		return errors.New("llm response: invalid json")
	}
	return nil
}

// ladder downgrades request features one step at a time when an endpoint
// rejects them with 400 (older models, OpenAI-compatible servers), and
// remembers the level that worked.
type ladder struct {
	level atomic.Int32
	max   int32
}

func (l *ladder) run(try func(level int32) error) error {
	for {
		lv := l.level.Load()
		err := try(lv)
		var se statusError
		if errors.As(err, &se) && se.code == http.StatusBadRequest && lv < l.max {
			l.level.CompareAndSwap(lv, lv+1)
			continue
		}
		return err
	}
}

// stripFences removes a Markdown code fence some models add when they are
// not constrained by a schema.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
