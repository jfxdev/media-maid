package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	requestTimeout  = 10 * time.Second
	maxResponseSize = 64 << 20
)

// ErrNotFound is returned when the server answers 404.
var ErrNotFound = errors.New("not found on media server")

// Client is a generic, platform-agnostic HTTP client for the configured server.
type Client struct {
	spec    PlatformSpec
	baseURL *url.URL
	token   string
	http    *http.Client
}

// NewClient builds a client for spec at baseURL authenticated with token.
func NewClient(spec PlatformSpec, baseURL, token string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid media server url: %w", err)
	}
	return &Client{
		spec:    spec,
		baseURL: u,
		token:   token,
		http:    &http.Client{Timeout: requestTimeout},
	}, nil
}

// Spec returns the platform spec this client talks to.
func (c *Client) Spec() PlatformSpec { return c.spec }

// Request performs an authenticated request. Errors never include the token.
func (c *Client) Request(ctx context.Context, method, path string, query url.Values) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	u, err := url.Parse(c.baseURL.String() + path)
	if err != nil {
		return nil, fmt.Errorf("invalid request path: %w", err)
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(c.spec.AuthHeader, c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, fmt.Errorf("%s %s: %w", method, path, uerr.Err)
		}
		return nil, fmt.Errorf("%s %s: request failed", method, path)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s %s: unexpected status %d", method, path, resp.StatusCode)
	}
	return body, nil
}

// Sync asks the server to rescan its libraries.
func (c *Client) Sync(ctx context.Context) error {
	_, err := c.Request(ctx, c.spec.Sync.Method, c.spec.Sync.Path, nil)
	return err
}

// ListItems returns every movie/episode the server knows about.
func (c *Client) ListItems(ctx context.Context) ([]ServerItem, error) {
	return c.spec.ListItems(ctx, c)
}

// ErrInvalidItemID is returned for IDs outside the expected character set.
var ErrInvalidItemID = errors.New("invalid item id")

var reItemID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// DeleteItem removes an item from the server library (never touches files).
func (c *Client) DeleteItem(ctx context.Context, id string) error {
	if !reItemID.MatchString(id) {
		return ErrInvalidItemID
	}
	p := strings.ReplaceAll(c.spec.DeleteItem.Path, "{id}", url.PathEscape(id))
	_, err := c.Request(ctx, c.spec.DeleteItem.Method, p, nil)
	return err
}
