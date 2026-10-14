// Package config loads and validates Media Maid configuration from the
// environment. Any invalid value aborts startup.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/notify"
	"github.com/jfxdev/media-maid/internal/safepath"
)

const (
	minAPITokenLen       = 32
	defaultVideoExts     = "mkv,mp4,avi,m4v,mov,wmv,ts,webm"
	defaultSubtitleExts  = "srt,ass,ssa,sub,idx,vtt"
	defaultSyncDebounce  = 30 * time.Second
	defaultStableSeconds = 30
	defaultListenAddr    = ":8080"
	defaultLLMMinConf    = 0.7
	defaultLLMTimeout    = 20 * time.Second
	maxLLMTimeout        = 2 * time.Minute
)

// LLM providers accepted in LLM_PROVIDER.
const (
	LLMOpenAI    = "openai"
	LLMAnthropic = "anthropic"
)

var reLanguage = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2}|-[0-9]{3})?$`)

// serverPrefixes are variables of specific servers, by integration mode.
// Media Maid is single tenant and only reads the generic MEDIA_SERVER_*
// variables: a variable of the configured server (often from an env file
// shared with that server, such as PLEX_CLAIM) is only a warning, one of
// another server is an error.
var serverPrefixes = map[string]string{"jellyfin": "JELLYFIN_", "emby": "EMBY_", "plex": "PLEX_"}

// PathMap translates the server's view of a path to the local one.
type PathMap struct {
	Server string
	Local  string
}

// ToServer maps a local path to the server's view of it, the inverse of
// Translate. Paths outside the local prefix are returned unchanged.
func (m *PathMap) ToServer(p string) string {
	p = filepath.Clean(p)
	if m == nil {
		return p
	}
	if safepath.Within(m.Local, p) {
		return filepath.Join(m.Server, strings.TrimPrefix(p, m.Local))
	}
	return p
}

// Translate maps a server path to a local path. Paths outside the server
// prefix are returned unchanged.
func (m *PathMap) Translate(p string) string {
	p = filepath.Clean(p)
	if m == nil {
		return p
	}
	if safepath.Within(m.Server, p) {
		return filepath.Join(m.Local, strings.TrimPrefix(p, m.Server))
	}
	return p
}

// Config is the validated runtime configuration.
type Config struct {
	Mode         string
	Platform     integration.PlatformSpec
	ServerURL    string
	ServerToken  string
	APIToken     string
	DownloadsDir string
	MoviesDir    string
	TVDir        string
	// StateDir keeps caches, history, the pending list and the organize
	// ledger across restarts.
	StateDir           string
	VideoExts          map[string]bool
	SubtitleExts       map[string]bool
	TMDBKey            string
	LLM                LLMConfig
	PreferredLanguage  string
	PathMap            *PathMap
	OrphanScanInterval time.Duration
	// ScanInterval runs a periodic downloads scan as a fallback for missed
	// filesystem events (network filesystems); zero disables it.
	ScanInterval   time.Duration
	SyncDebounce   time.Duration
	StableDuration time.Duration
	ListenAddr     string
	DryRun         bool
	// CopyFallback copies a file that cannot be hardlinked (another
	// filesystem or mount); false (the default) makes that an error.
	CopyFallback bool
	// OrganizeTrigger is TriggerWatch (the watcher and SCAN_INTERVAL
	// organize finished downloads) or TriggerHook (only API requests, e.g.
	// a torrent client's "download finished" hook).
	OrganizeTrigger string
	// AmbiguousBest accepts the best TMDB candidate when it is weak or tied
	// (AMBIGUOUS_MATCH=best) instead of leaving the file pending.
	AmbiguousBest bool
	// AutoUpgrade lets automatic runs replace a release Media Maid placed
	// with a better one of the same title.
	AutoUpgrade bool
	// LinkExtras links extras folders of a release as server extras.
	LinkExtras bool
	// Notify is the optional webhook; URL is empty when it is disabled.
	Notify NotifyConfig
	// Warnings are non-fatal configuration notes to log at startup.
	Warnings []string
}

// ORGANIZE_TRIGGER values.
const (
	TriggerWatch = "watch"
	TriggerHook  = "hook"
)

// NotifyConfig configures the notification webhook. URL may carry a token:
// it is a secret.
type NotifyConfig struct {
	URL    string
	Format string
	On     map[string]bool
}

// LLMConfig configures the optional LLM decision layer. Provider is empty
// when the layer is disabled.
type LLMConfig struct {
	Provider      string
	BaseURL       string
	APIKey        string
	Model         string
	MinConfidence float64
	Timeout       time.Duration
}

// Roots returns the configured directories that must not overlap, by name.
func (c *Config) Roots() map[string]string {
	r := map[string]string{"DOWNLOADS_DIR": c.DownloadsDir, "MOVIES_DIR": c.MoviesDir, "TV_DIR": c.TVDir}
	if c.StateDir != "" {
		r["STATE_DIR"] = c.StateDir
	}
	return r
}

// SyncEnabled reports whether a media server is configured.
func (c *Config) SyncEnabled() bool { return c.ServerURL != "" }

// Load reads configuration using getenv and validates it. environ is the full
// environment (os.Environ()) used to reject variables of other servers.
func Load(getenv func(string) string, environ []string) (*Config, error) {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	c := &Config{}

	mode := strings.ToLower(strings.TrimSpace(getenv("INTEGRATION_MODE")))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		for m, p := range serverPrefixes {
			if !strings.HasPrefix(name, p) {
				continue
			}
			if m == mode {
				c.Warnings = append(c.Warnings, name+" is ignored: Media Maid reads MEDIA_SERVER_URL and MEDIA_SERVER_TOKEN")
			} else {
				fail("%s is not supported: Media Maid is single tenant (INTEGRATION_MODE=%s) and uses MEDIA_SERVER_URL/MEDIA_SERVER_TOKEN; run one sidecar per media server", name, mode)
			}
		}
	}

	c.Mode = mode
	switch {
	case c.Mode == "":
		fail("INTEGRATION_MODE is required (one of %s)", strings.Join(integration.Modes(), ", "))
	case hasMultiple(c.Mode):
		fail("INTEGRATION_MODE must be a single value: Media Maid is bound to exactly one media server")
	default:
		spec, ok := integration.Lookup(c.Mode)
		if !ok {
			fail("INTEGRATION_MODE %q is not supported (one of %s)", c.Mode, strings.Join(integration.Modes(), ", "))
		}
		c.Platform = spec
	}

	c.ServerURL = strings.TrimRight(strings.TrimSpace(getenv("MEDIA_SERVER_URL")), "/")
	c.ServerToken = strings.TrimSpace(getenv("MEDIA_SERVER_TOKEN"))
	switch {
	case (c.ServerURL == "") != (c.ServerToken == ""):
		fail("MEDIA_SERVER_URL and MEDIA_SERVER_TOKEN must be set together (or both left empty to disable sync)")
	case c.ServerURL != "":
		if hasMultiple(c.ServerURL) {
			fail("MEDIA_SERVER_URL must be a single address: Media Maid is bound to exactly one media server")
		} else if u, err := url.Parse(c.ServerURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fail("MEDIA_SERVER_URL must be an http(s) URL")
		}
		if hasMultiple(c.ServerToken) {
			fail("MEDIA_SERVER_TOKEN must be a single value")
		}
	}

	c.APIToken = getenv("API_TOKEN")
	if len(c.APIToken) < minAPITokenLen {
		fail("API_TOKEN is required and must be at least %d characters", minAPITokenLen)
	}

	c.DownloadsDir = requireAbs("DOWNLOADS_DIR", getenv, fail)
	c.MoviesDir = requireAbs("MOVIES_DIR", getenv, fail)
	c.TVDir = requireAbs("TV_DIR", getenv, fail)
	// Without the ledger on disk, a restart would link again files the user
	// removed from the library: STATE_DIR is required.
	c.StateDir = requireAbs("STATE_DIR", getenv, fail)
	if c.DownloadsDir != "" && c.MoviesDir != "" && c.TVDir != "" {
		if err := DistinctRoots(c.Roots()); err != nil {
			errs = append(errs, err)
		}
	}

	var err error
	if c.VideoExts, err = parseExts(getenvDefault(getenv, "VIDEO_EXTENSIONS", defaultVideoExts)); err != nil {
		fail("VIDEO_EXTENSIONS: %v", err)
	}
	if c.SubtitleExts, err = parseExts(getenvDefault(getenv, "SUBTITLE_EXTENSIONS", defaultSubtitleExts)); err != nil {
		fail("SUBTITLE_EXTENSIONS: %v", err)
	}
	for e := range c.VideoExts {
		if c.SubtitleExts[e] {
			fail("extension %q cannot be both a video and a subtitle extension", e)
		}
	}

	c.TMDBKey = strings.TrimSpace(getenv("TMDB_API_KEY"))

	c.LLM = loadLLM(getenv, fail)

	if v := strings.TrimSpace(getenv("PREFERRED_LANGUAGE")); v != "" {
		if !reLanguage.MatchString(v) {
			fail("PREFERRED_LANGUAGE must be a single BCP 47 tag such as pt-BR or es")
		}
		c.PreferredLanguage = v
	}

	if v := strings.TrimSpace(getenv("SERVER_PATH_MAP")); v != "" {
		pm, err := parsePathMap(v)
		if err != nil {
			fail("SERVER_PATH_MAP: %v", err)
		}
		c.PathMap = pm
	}

	c.OrphanScanInterval = parseDuration("ORPHAN_SCAN_INTERVAL", getenv, 0, fail)
	if c.OrphanScanInterval < 0 || (c.OrphanScanInterval > 0 && c.OrphanScanInterval < time.Minute) {
		fail("ORPHAN_SCAN_INTERVAL must be empty or at least 1m")
	}
	c.ScanInterval = parseDuration("SCAN_INTERVAL", getenv, 0, fail)
	if c.ScanInterval < 0 || (c.ScanInterval > 0 && c.ScanInterval < time.Minute) {
		fail("SCAN_INTERVAL must be empty or at least 1m")
	}
	c.SyncDebounce = parseDuration("SYNC_DEBOUNCE", getenv, defaultSyncDebounce, fail)
	if c.SyncDebounce < 0 {
		fail("SYNC_DEBOUNCE must not be negative")
	}

	stable := defaultStableSeconds
	if v := strings.TrimSpace(getenv("STABLE_SECONDS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail("STABLE_SECONDS must be a positive integer")
		}
		stable = n
	}
	c.StableDuration = time.Duration(stable) * time.Second

	c.ListenAddr = getenvDefault(getenv, "LISTEN_ADDR", defaultListenAddr)

	c.DryRun = parseBool("DRY_RUN", getenv, false, fail)

	// Copying uses as much disk space again: opt-in only.
	c.CopyFallback = parseBool("COPY_FALLBACK", getenv, false, fail)
	c.OrganizeTrigger = parseChoice("ORGANIZE_TRIGGER", getenv, TriggerWatch, fail, TriggerWatch, TriggerHook)
	if c.OrganizeTrigger == TriggerHook && c.ScanInterval > 0 {
		fail("SCAN_INTERVAL has no effect with ORGANIZE_TRIGGER=hook: leave it empty")
	}
	c.AmbiguousBest = parseChoice("AMBIGUOUS_MATCH", getenv, "pending", fail, "pending", "best") == "best"
	c.AutoUpgrade = parseBool("AUTO_UPGRADE", getenv, false, fail)
	c.LinkExtras = parseBool("LINK_EXTRAS", getenv, true, fail)
	c.Notify = loadNotify(getenv, fail)

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// parseChoice reads name as one of choices (case-insensitive), def when empty.
func parseChoice(name string, getenv func(string) string, def string, fail func(string, ...any), choices ...string) string {
	v := strings.ToLower(strings.TrimSpace(getenv(name)))
	if v == "" {
		return def
	}
	if !slices.Contains(choices, v) {
		fail("%s must be one of %s", name, strings.Join(choices, ", "))
		return def
	}
	return v
}

func parseBool(name string, getenv func(string) string, def bool, fail func(string, ...any)) bool {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		fail("%s must be a boolean", name)
		return def
	}
	return b
}

// loadNotify reads NOTIFY_URL, NOTIFY_FORMAT and NOTIFY_ON. The URL never
// appears in an error: it may carry a token.
func loadNotify(getenv func(string) string, fail func(string, ...any)) NotifyConfig {
	n := NotifyConfig{URL: strings.TrimSpace(getenv("NOTIFY_URL"))}
	format := strings.ToLower(strings.TrimSpace(getenv("NOTIFY_FORMAT")))
	on := strings.TrimSpace(getenv("NOTIFY_ON"))
	if n.URL == "" {
		if format != "" || on != "" {
			fail("NOTIFY_FORMAT and NOTIFY_ON require NOTIFY_URL")
		}
		return NotifyConfig{}
	}
	if u, err := url.Parse(n.URL); err != nil || hasMultiple(n.URL) || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		fail("NOTIFY_URL must be a single http(s) URL")
	}
	switch format {
	case "":
		n.Format = notify.FormatJSON
	case notify.FormatJSON, notify.FormatText:
		n.Format = format
	default:
		fail("NOTIFY_FORMAT must be %s or %s", notify.FormatJSON, notify.FormatText)
	}
	if on == "" {
		on = notify.DefaultOn
	}
	valid := map[string]bool{}
	for _, s := range notify.Statuses {
		valid[s] = true
	}
	n.On = map[string]bool{}
	for _, s := range strings.Split(on, ",") {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if !valid[s] {
			fail("NOTIFY_ON: unknown status %q (one of %s)", s, strings.Join(notify.Statuses, ", "))
			continue
		}
		n.On[s] = true
	}
	if len(n.On) == 0 {
		fail("NOTIFY_ON must list at least one status")
	}
	return n
}

func loadLLM(getenv func(string) string, fail func(string, ...any)) LLMConfig {
	l := LLMConfig{MinConfidence: defaultLLMMinConf, Timeout: defaultLLMTimeout}
	l.Provider = strings.ToLower(strings.TrimSpace(getenv("LLM_PROVIDER")))
	l.BaseURL = strings.TrimRight(strings.TrimSpace(getenv("LLM_BASE_URL")), "/")
	l.APIKey = strings.TrimSpace(getenv("LLM_API_KEY"))
	l.Model = strings.TrimSpace(getenv("LLM_MODEL"))
	// The threshold also applies without the LLM: an unsure reading that
	// TMDB has no match for waits as pending.
	if v := strings.TrimSpace(getenv("LLM_MIN_CONFIDENCE")); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			fail("LLM_MIN_CONFIDENCE must be a number between 0 and 1")
		}
		l.MinConfidence = f
	}
	if l.Provider == "" {
		if l.BaseURL != "" || l.APIKey != "" || l.Model != "" {
			fail("LLM_BASE_URL, LLM_API_KEY and LLM_MODEL require LLM_PROVIDER (%s or %s)", LLMOpenAI, LLMAnthropic)
		}
		return LLMConfig{MinConfidence: l.MinConfidence}
	}
	switch l.Provider {
	case LLMOpenAI, LLMAnthropic:
	default:
		if hasMultiple(l.Provider) {
			fail("LLM_PROVIDER must be a single value")
		} else {
			fail("LLM_PROVIDER %q is not supported (%s or %s)", l.Provider, LLMOpenAI, LLMAnthropic)
		}
	}
	if l.BaseURL != "" {
		if u, err := url.Parse(l.BaseURL); err != nil || hasMultiple(l.BaseURL) || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fail("LLM_BASE_URL must be a single http(s) URL")
		}
	}
	// The public endpoints always need a key; a custom base URL (Ollama, a
	// gateway) may not.
	if l.APIKey == "" && l.BaseURL == "" {
		fail("LLM_API_KEY is required unless LLM_BASE_URL points to a server that needs none")
	}
	if hasMultiple(l.APIKey) {
		fail("LLM_API_KEY must be a single value")
	}
	// The public OpenAI endpoint has a default model; another server
	// (Ollama, LM Studio, a gateway) names its own.
	if l.Model == "" && l.Provider == LLMOpenAI && l.BaseURL != "" {
		fail("LLM_MODEL is required with LLM_PROVIDER=openai and LLM_BASE_URL")
	}
	if hasMultiple(l.Model) {
		fail("LLM_MODEL must be a single value")
	}
	l.Timeout = parseDuration("LLM_TIMEOUT", getenv, defaultLLMTimeout, fail)
	if l.Timeout <= 0 || l.Timeout > maxLLMTimeout {
		fail("LLM_TIMEOUT must be between 1s and 2m")
	}
	return l
}

// DistinctRoots fails if any two roots are equal or nested.
func DistinctRoots(roots map[string]string) error {
	names := make([]string, 0, len(roots))
	for n := range roots {
		names = append(names, n)
	}
	for i := 0; i < len(names); i++ {
		for j := 0; j < len(names); j++ {
			if i == j {
				continue
			}
			a, b := roots[names[i]], roots[names[j]]
			if safepath.Within(a, b) {
				return fmt.Errorf("%s and %s must be distinct, non-nested directories", names[i], names[j])
			}
		}
	}
	return nil
}

func requireAbs(name string, getenv func(string) string, fail func(string, ...any)) string {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		fail("%s is required", name)
		return ""
	}
	if !filepath.IsAbs(v) || strings.ContainsRune(v, 0) {
		fail("%s must be an absolute path", name)
		return ""
	}
	return filepath.Clean(v)
}

func getenvDefault(getenv func(string) string, name, def string) string {
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v
	}
	return def
}

func parseDuration(name string, getenv func(string) string, def time.Duration, fail func(string, ...any)) time.Duration {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		fail("%s must be a duration (e.g. 30s, 24h)", name)
		return def
	}
	return d
}

func parseExts(v string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, p := range strings.Split(v, ",") {
		e := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p), "."))
		if e == "" {
			continue
		}
		for _, r := range e {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return nil, fmt.Errorf("invalid extension %q", p)
			}
		}
		out["."+e] = true
	}
	if len(out) == 0 {
		return nil, errors.New("at least one extension is required")
	}
	return out, nil
}

func parsePathMap(v string) (*PathMap, error) {
	if hasMultiple(v) || strings.Count(v, "=") != 1 {
		return nil, errors.New(`must be a single "server_prefix=local_prefix" pair`)
	}
	s, l, _ := strings.Cut(v, "=")
	s, l = strings.TrimSpace(s), strings.TrimSpace(l)
	if !filepath.IsAbs(s) || !filepath.IsAbs(l) || strings.ContainsRune(v, 0) {
		return nil, errors.New("both prefixes must be absolute paths")
	}
	return &PathMap{Server: filepath.Clean(s), Local: filepath.Clean(l)}, nil
}

// hasMultiple reports whether a value looks like a list of values.
func hasMultiple(v string) bool {
	return strings.ContainsAny(v, ",; \t\n\r")
}
