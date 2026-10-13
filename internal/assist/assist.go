// Package assist is the optional LLM decision layer. It is consulted only
// when the parser and TMDB disagree or are unsure, and as a last resort for
// localized titles. Everything it returns is validated against the release
// name and the TMDB data before use; it never decides paths or IDs.
package assist

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/parser"
)

// Providers accepted by New.
const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
)

// Request asks the model to settle an ambiguous identification.
type Request struct {
	// Path is the release path relative to the downloads root.
	Path       string
	Parsed     parser.Analysis
	Candidates []metadata.Result
}

// Decision is a validated answer. Choice indexes Request.Candidates, or is -1.
type Decision struct {
	Kind       parser.Kind
	Title      string
	Year       int
	Season     int
	Episodes   []int
	Choice     int
	Confidence float64
}

// LocalizeRequest asks for the official title in Language.
type LocalizeRequest struct {
	Kind          metadata.Kind
	Title         string
	OriginalTitle string
	Year          int
	Language      string
	// Options are TMDB titles; when present the answer must be one of them.
	Options []metadata.Title
}

// Resolver is the decision layer as seen by the organizer. A nil Resolver
// means the layer is disabled.
type Resolver interface {
	Resolve(ctx context.Context, r Request) (Decision, error)
	Localize(ctx context.Context, r LocalizeRequest) (string, error)
}

// ErrRejected wraps answers that failed validation.
var ErrRejected = errors.New("llm answer rejected")

// ErrUnavailable is returned while the circuit breaker is open.
var ErrUnavailable = errors.New("llm temporarily disabled after repeated failures")

// Options configures a Client.
type Options struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Timeout  time.Duration
	// MinConfidence is the lowest self-reported confidence accepted.
	MinConfidence float64
	Log           *slog.Logger
}

const (
	maxCandidates    = metadata.MaxCandidates
	maxOptions       = 30
	maxPathRunes     = 300
	maxTitleRunes    = 200
	cacheSize        = 4096
	breakerThreshold = 5
	breakerCooldown  = 5 * time.Minute
)

// Client implements Resolver over an OpenAI-compatible or Anthropic endpoint.
type Client struct {
	be       backend
	provider string
	model    string
	minConf  float64
	log      *slog.Logger
	now      func() time.Time

	mu        sync.Mutex
	cache     map[[32]byte][]byte
	failures  int
	openUntil time.Time

	// OnChange, when set, is called (without locks held) after the cache
	// gains an entry, so the caller can persist Export.
	OnChange func()
}

// New builds a Client.
func New(o Options) (*Client, error) {
	hc := &http.Client{Timeout: o.Timeout}
	if o.Timeout <= 0 {
		hc.Timeout = 20 * time.Second
	}
	c := &Client{provider: o.Provider, minConf: o.MinConfidence, log: o.Log, now: time.Now, cache: map[[32]byte][]byte{}}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	switch o.Provider {
	case ProviderOpenAI:
		b := newOpenAI(o.BaseURL, o.APIKey, o.Model, hc)
		c.be, c.model = b, b.model
	case ProviderAnthropic:
		b := newAnthropic(o.BaseURL, o.APIKey, o.Model, hc)
		c.be, c.model = b, b.model
	default:
		return nil, fmt.Errorf("unknown llm provider %q", o.Provider)
	}
	return c, nil
}

// Model returns the configured model name (for startup logs).
func (c *Client) Model() string { return c.model }

type wireCandidate struct {
	Index         int    `json:"index"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title,omitempty"`
	Year          int    `json:"year,omitempty"`
}

type wireMedia struct {
	Kind     parser.Kind `json:"kind"`
	Title    string      `json:"title"`
	Year     int         `json:"year,omitempty"`
	Season   int         `json:"season,omitempty"`
	Episodes []int       `json:"episodes,omitempty"`
	AirDate  string      `json:"air_date,omitempty"`
}

type wireParsed struct {
	wireMedia
	Confidence   float64     `json:"confidence"`
	Alternatives []wireMedia `json:"alternatives,omitempty"`
}

func toWire(m parser.Media) wireMedia {
	return wireMedia{Kind: m.Kind, Title: clean(m.Title, maxTitleRunes), Year: m.Year, Season: m.Season, Episodes: m.Episodes, AirDate: m.AirDate}
}

// Resolve asks the model to pick a candidate or confirm the parse.
func (c *Client) Resolve(ctx context.Context, r Request) (Decision, error) {
	in := struct {
		Path       string          `json:"path"`
		Parser     wireParsed      `json:"parser"`
		Candidates []wireCandidate `json:"candidates"`
		MaxYear    int             `json:"max_year"`
	}{Path: shortPath(r.Path), MaxYear: c.now().Year() + 1, Candidates: []wireCandidate{}}
	in.Parser = wireParsed{wireMedia: toWire(r.Parsed.Media), Confidence: r.Parsed.Confidence}
	for _, a := range r.Parsed.Alternatives {
		in.Parser.Alternatives = append(in.Parser.Alternatives, toWire(a))
	}
	if len(r.Candidates) > maxCandidates {
		r.Candidates = r.Candidates[:maxCandidates]
	}
	for i, cand := range r.Candidates {
		in.Candidates = append(in.Candidates, wireCandidate{Index: i, Title: clean(cand.Title, maxTitleRunes), OriginalTitle: clean(cand.OriginalTitle, maxTitleRunes), Year: cand.Year})
	}

	var out struct {
		Kind       string  `json:"kind"`
		Title      string  `json:"title"`
		Year       int     `json:"year"`
		Season     int     `json:"season"`
		Episodes   []int   `json:"episodes"`
		Choice     int     `json:"choice"`
		Confidence float64 `json:"confidence"`
	}
	start := c.now()
	err := c.ask(ctx, taskDisambiguate, in, &out)
	var d Decision
	if err == nil {
		d = Decision{Kind: parser.Kind(out.Kind), Title: out.Title, Year: out.Year, Season: out.Season, Episodes: out.Episodes, Choice: out.Choice, Confidence: out.Confidence}
		err = validateDecision(&d, r, c.minConf, c.now().Year()+1)
	}
	c.logResult(taskDisambiguate, start, err)
	return d, err
}

// Localize asks for the official title in r.Language. It returns "" with a
// nil error when the model knows none.
func (c *Client) Localize(ctx context.Context, r LocalizeRequest) (string, error) {
	type option struct {
		Index    int    `json:"index"`
		Language string `json:"language,omitempty"`
		Country  string `json:"country,omitempty"`
		Title    string `json:"title"`
	}
	in := struct {
		Work struct {
			Kind          metadata.Kind `json:"kind"`
			OriginalTitle string        `json:"original_title,omitempty"`
			Title         string        `json:"title"`
			Year          int           `json:"year,omitempty"`
		} `json:"work"`
		Language string   `json:"language"`
		Options  []option `json:"options"`
	}{Language: clean(r.Language, 20), Options: []option{}}
	in.Work.Kind, in.Work.Title, in.Work.OriginalTitle, in.Work.Year = r.Kind, clean(r.Title, maxTitleRunes), clean(r.OriginalTitle, maxTitleRunes), r.Year
	if len(r.Options) > maxOptions {
		r.Options = r.Options[:maxOptions]
	}
	for i, o := range r.Options {
		in.Options = append(in.Options, option{Index: i, Language: clean(o.Language, 3), Country: clean(o.Country, 3), Title: clean(o.Title, maxTitleRunes)})
	}

	var out struct {
		LocalizedTitle string  `json:"localized_title"`
		Choice         int     `json:"choice"`
		Confidence     float64 `json:"confidence"`
	}
	start := c.now()
	err := c.ask(ctx, taskLocalize, in, &out)
	title := ""
	if err == nil {
		title, err = validateLocalized(out.LocalizedTitle, out.Choice, out.Confidence, r.Options, c.minConf)
	}
	c.logResult(taskLocalize, start, err)
	return title, err
}

// ask serializes in, consults the cache and the circuit breaker, calls the
// backend and strictly decodes the answer into out.
func (c *Client) ask(ctx context.Context, t task, in, out any) error {
	user, err := json.Marshal(in)
	if err != nil {
		return err
	}
	key := sha256.Sum256(append([]byte(promptVersion+"|"+t.name+"|"+c.model+"|"), user...))

	c.mu.Lock()
	cached, hit := c.cache[key]
	open := c.now().Before(c.openUntil)
	c.mu.Unlock()
	if !hit {
		if open {
			return ErrUnavailable
		}
		cached, err = c.be.complete(ctx, t, user)
		c.mu.Lock()
		switch {
		case err != nil && ctx.Err() == nil:
			c.failures++
			if c.failures >= breakerThreshold {
				c.openUntil = c.now().Add(breakerCooldown)
				c.failures = 0
				c.log.Warn("llm disabled after repeated failures", "cooldown", breakerCooldown.String())
			}
		case err == nil:
			c.failures = 0
		}
		c.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if err := decodeStrict(cached, out); err != nil {
		return fmt.Errorf("%w: %v", ErrRejected, err)
	}
	if !hit {
		// Only well-formed answers are cached; validation is re-run on hits.
		c.mu.Lock()
		if len(c.cache) >= cacheSize {
			clear(c.cache)
		}
		c.cache[key] = cached
		c.mu.Unlock()
		if c.OnChange != nil {
			c.OnChange()
		}
	}
	return nil
}

// Export returns the answer cache as JSON. Keys are hashes that include the
// prompt version and model, so a changed prompt or model never reuses an old
// answer; answers are validated again whenever they are used.
func (c *Client) Export() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[string][]byte, len(c.cache))
	for k, v := range c.cache {
		m[hex.EncodeToString(k[:])] = v
	}
	return json.Marshal(m)
}

// Import loads a cache saved by Export.
func (c *Client) Import(data []byte) error {
	var m map[string][]byte
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range m {
		var key [32]byte
		if b, err := hex.DecodeString(k); err == nil && len(b) == len(key) && len(c.cache) < cacheSize {
			copy(key[:], b)
			c.cache[key] = v
		}
	}
	return nil
}

func (c *Client) logResult(t task, start time.Time, err error) {
	outcome := "accepted"
	if err != nil {
		outcome = "rejected: " + err.Error()
	}
	c.log.Info("llm", "task", t.name, "provider", c.provider, "model", c.model,
		"prompt_version", promptVersion, "ms", c.now().Sub(start).Milliseconds(), "outcome", outcome)
}

// decodeStrict accepts exactly one JSON object with no unknown fields.
func decodeStrict(b []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errors.New("answer is not the expected JSON object")
	}
	if dec.More() {
		return errors.New("trailing data after the JSON object")
	}
	return nil
}

// shortPath keeps the file name and up to two parent folders.
func shortPath(p string) string {
	parts := strings.Split(path.Clean(strings.ReplaceAll(p, "\\", "/")), "/")
	if len(parts) > 3 {
		parts = parts[len(parts)-3:]
	}
	return clean(strings.Join(parts, "/"), maxPathRunes)
}

// clean drops control and invisible format characters, collapses whitespace
// and caps the length, so untrusted names cannot smuggle layout tricks into
// the prompt.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r):
			if unicode.IsSpace(r) {
				return ' '
			}
			return -1
		case unicode.IsSpace(r):
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max])
	}
	return s
}
