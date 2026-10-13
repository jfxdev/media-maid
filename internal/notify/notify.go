// Package notify posts organize outcomes that need attention to a webhook
// (NOTIFY_URL): a generic JSON document, which the Apprise API accepts as is,
// or plain text, which ntfy shows as a message.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Formats accepted in NOTIFY_FORMAT.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// Statuses that can be selected in NOTIFY_ON.
var Statuses = []string{"linked", "copied", "replaced", "upgraded", "conflict", "duplicate", "skipped", "error"}

// DefaultOn is the NOTIFY_ON default: what needs a decision, plus upgrades.
const DefaultOn = "conflict,error,skipped,upgraded"

// Event is one organize outcome.
type Event struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Dest   string    `json:"dest,omitempty"`
	Kind   string    `json:"kind,omitempty"`
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
}

// Options configures a Notifier.
type Options struct {
	URL    string
	Format string
	// On lists the statuses that are sent.
	On map[string]bool
	// Delay groups the events of a burst (a season pack) into one message.
	Delay  time.Duration
	Client *http.Client
	Log    *slog.Logger
}

// Notifier batches events and posts them. Delivery is best effort: a failed
// post is logged (without the URL, which may carry a token) and dropped.
type Notifier struct {
	url    string
	format string
	on     map[string]bool
	delay  time.Duration
	client *http.Client
	log    *slog.Logger

	mu    sync.Mutex
	batch []Event
	timer *time.Timer
	// sendMu serializes sends, so Flush waits for one in flight.
	sendMu sync.Mutex
}

// maxBatch bounds a message; further events of the burst are counted only.
const maxBatch = 50

// New builds a Notifier.
func New(o Options) *Notifier {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 15 * time.Second}
	}
	if o.Format == "" {
		o.Format = FormatJSON
	}
	return &Notifier{url: o.URL, format: o.Format, on: o.On, delay: o.Delay, client: o.Client, log: o.Log}
}

// Notify queues e when its status is selected.
func (n *Notifier) Notify(e Event) {
	if n == nil || !n.on[e.Status] {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.batch = append(n.batch, e)
	if n.timer == nil {
		n.timer = time.AfterFunc(n.delay, n.fire)
	}
}

func (n *Notifier) fire() {
	n.sendMu.Lock()
	defer n.sendMu.Unlock()
	n.mu.Lock()
	batch := n.batch
	n.batch, n.timer = nil, nil
	n.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := n.send(ctx, batch); err != nil {
		n.log.Warn("notification failed", "events", len(batch), "err", err)
	}
}

// Flush sends what is queued now and waits for sends in flight; call it at
// shutdown.
func (n *Notifier) Flush() {
	if n == nil {
		return
	}
	n.mu.Lock()
	if n.timer != nil {
		n.timer.Stop()
	}
	n.mu.Unlock()
	n.fire()
}

// payload is the JSON document: title and body for services that show a
// message (Apprise), events for scripts.
type payload struct {
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	Events []Event `json:"events"`
}

func render(batch []Event) payload {
	extra := 0
	if len(batch) > maxBatch {
		extra = len(batch) - maxBatch
		batch = batch[:maxBatch]
	}
	title := "Media Maid: " + summary(batch, extra)
	var b strings.Builder
	for _, e := range batch {
		fmt.Fprintf(&b, "%s: %s", e.Status, e.Source)
		if e.Dest != "" {
			fmt.Fprintf(&b, " -> %s", e.Dest)
		}
		if e.Error != "" {
			fmt.Fprintf(&b, " (%s)", e.Error)
		}
		b.WriteByte('\n')
	}
	if extra > 0 {
		fmt.Fprintf(&b, "... and %d more\n", extra)
	}
	return payload{Title: title, Body: strings.TrimRight(b.String(), "\n"), Events: batch}
}

// headerSafe drops control characters, which file names may hold but a
// header value may not.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func summary(batch []Event, extra int) string {
	if len(batch) == 1 && extra == 0 {
		return batch[0].Status + " " + batch[0].Source
	}
	return fmt.Sprintf("%d organize events", len(batch)+extra)
}

func (n *Notifier) send(ctx context.Context, batch []Event) error {
	p := render(batch)
	var body []byte
	ctype := "application/json"
	if n.format == FormatText {
		body, ctype = []byte(p.Body), "text/plain; charset=utf-8"
	} else {
		var err error
		if body, err = json.Marshal(p); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid request")
	}
	req.Header.Set("Content-Type", ctype)
	if n.format == FormatText {
		req.Header.Set("Title", headerSafe(p.Title)) // ntfy
	}
	resp, err := n.client.Do(req)
	if err != nil {
		// The error text of net/http holds the URL; it is not logged.
		return errors.New("request failed")
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
