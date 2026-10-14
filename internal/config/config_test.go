package config

import (
	"strings"
	"testing"
)

func baseEnv() map[string]string {
	return map[string]string{
		"INTEGRATION_MODE":   "jellyfin",
		"MEDIA_SERVER_URL":   "http://jellyfin:8096/",
		"MEDIA_SERVER_TOKEN": "abc",
		"API_TOKEN":          strings.Repeat("x", 32),
		"DOWNLOADS_DIR":      "/data/downloads",
		"MOVIES_DIR":         "/data/media/movies",
		"TV_DIR":             "/data/media/tv",
		"STATE_DIR":          "/data/state",
	}
}

func load(env map[string]string, extra ...string) (*Config, error) {
	var environ []string
	for k, v := range env {
		environ = append(environ, k+"="+v)
	}
	environ = append(environ, extra...)
	return Load(func(k string) string { return env[k] }, environ)
}

func TestLoadValid(t *testing.T) {
	c, err := load(baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != "jellyfin" || c.ServerURL != "http://jellyfin:8096" || !c.SyncEnabled() {
		t.Errorf("unexpected config: %+v", c)
	}
	if !c.VideoExts[".mkv"] || !c.SubtitleExts[".srt"] || c.SyncDebounce.Seconds() != 30 || c.CopyFallback ||
		c.AutoUpgrade || !c.LinkExtras || c.Notify.URL != "" || c.OrganizeTrigger != TriggerWatch || c.AmbiguousBest {
		t.Errorf("defaults not applied")
	}
	env := baseEnv()
	env["COPY_FALLBACK"] = "true"
	env["ORGANIZE_TRIGGER"] = "Hook"
	env["AMBIGUOUS_MATCH"] = "best"
	if c, err := load(env); err != nil || !c.CopyFallback || c.OrganizeTrigger != TriggerHook || !c.AmbiguousBest {
		t.Errorf("opt-ins: %+v %v", c, err)
	}
	for _, mode := range []string{"emby", "plex", "PLEX"} {
		env := baseEnv()
		env["INTEGRATION_MODE"] = mode
		if _, err := load(env); err != nil {
			t.Errorf("mode %s: %v", mode, err)
		}
	}
}

func TestLoadLLM(t *testing.T) {
	c, err := load(baseEnv())
	if err != nil || c.LLM.Provider != "" || c.PreferredLanguage != "" {
		t.Fatalf("llm must be off by default: %+v %v", c.LLM, err)
	}
	valid := []map[string]string{
		{"LLM_PROVIDER": "anthropic", "LLM_API_KEY": "k"},
		{"LLM_PROVIDER": "OpenAI", "LLM_API_KEY": "k", "LLM_MODEL": "gpt-x"},
		{"LLM_PROVIDER": "openai", "LLM_API_KEY": "k"}, // public endpoint: default model
		{"LLM_PROVIDER": "openai", "LLM_BASE_URL": "http://ollama:11434/v1/", "LLM_MODEL": "llama3", "LLM_MIN_CONFIDENCE": "0.5", "LLM_TIMEOUT": "45s"},
		{"PREFERRED_LANGUAGE": "pt-BR"},
		{"PREFERRED_LANGUAGE": "es-419"},
		{"PREFERRED_LANGUAGE": "fr"},
	}
	for _, extra := range valid {
		env := baseEnv()
		for k, v := range extra {
			env[k] = v
		}
		if _, err := load(env); err != nil {
			t.Errorf("%v: %v", extra, err)
		}
	}
	c, _ = load(map[string]string{"INTEGRATION_MODE": "jellyfin", "API_TOKEN": strings.Repeat("x", 32), "DOWNLOADS_DIR": "/d", "MOVIES_DIR": "/m", "TV_DIR": "/t", "STATE_DIR": "/s",
		"LLM_PROVIDER": "openai", "LLM_BASE_URL": "http://ollama:11434/v1/", "LLM_MODEL": "llama3", "LLM_MIN_CONFIDENCE": "0.5", "LLM_TIMEOUT": "45s"})
	if c == nil || c.LLM.BaseURL != "http://ollama:11434/v1" || c.LLM.MinConfidence != 0.5 || c.LLM.Timeout.Seconds() != 45 {
		t.Errorf("llm config = %+v", c)
	}
	// The confidence threshold applies without the LLM too (an unsure
	// reading TMDB does not know waits as pending): defaulted, and settable.
	if c, err := load(baseEnv()); err != nil || c.LLM.Provider != "" || c.LLM.MinConfidence != 0.7 {
		t.Errorf("min confidence without llm = %+v, %v", c.LLM, err)
	}
	env := baseEnv()
	env["LLM_MIN_CONFIDENCE"] = "0.4"
	if c, err := load(env); err != nil || c.LLM.Provider != "" || c.LLM.MinConfidence != 0.4 {
		t.Errorf("LLM_MIN_CONFIDENCE without llm = %+v, %v", c.LLM, err)
	}

	invalid := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"LLM_PROVIDER": "gemini", "LLM_API_KEY": "k"}, "LLM_PROVIDER"},
		{map[string]string{"LLM_PROVIDER": "openai,anthropic", "LLM_API_KEY": "k"}, "single value"},
		{map[string]string{"LLM_PROVIDER": "anthropic"}, "LLM_API_KEY is required"},
		{map[string]string{"LLM_PROVIDER": "openai", "LLM_BASE_URL": "http://ollama:11434/v1"}, "LLM_MODEL is required"},
		{map[string]string{"LLM_MODEL": "x"}, "require LLM_PROVIDER"},
		{map[string]string{"LLM_PROVIDER": "anthropic", "LLM_API_KEY": "k", "LLM_BASE_URL": "ftp://x"}, "LLM_BASE_URL"},
		{map[string]string{"LLM_PROVIDER": "anthropic", "LLM_API_KEY": "k", "LLM_MIN_CONFIDENCE": "2"}, "LLM_MIN_CONFIDENCE"},
		{map[string]string{"LLM_MIN_CONFIDENCE": "x"}, "LLM_MIN_CONFIDENCE"},
		{map[string]string{"LLM_MIN_CONFIDENCE": "NaN"}, "LLM_MIN_CONFIDENCE"},
		{map[string]string{"LLM_PROVIDER": "anthropic", "LLM_API_KEY": "k", "LLM_MIN_CONFIDENCE": "nan"}, "LLM_MIN_CONFIDENCE"},
		{map[string]string{"LLM_PROVIDER": "anthropic", "LLM_API_KEY": "k", "LLM_TIMEOUT": "1h"}, "LLM_TIMEOUT"},
		{map[string]string{"PREFERRED_LANGUAGE": "portuguese"}, "PREFERRED_LANGUAGE"},
		{map[string]string{"PREFERRED_LANGUAGE": "pt-BR,en"}, "PREFERRED_LANGUAGE"},
	}
	for _, tt := range invalid {
		env := baseEnv()
		for k, v := range tt.env {
			env[k] = v
		}
		_, err := load(env)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want %q", tt.env, err, tt.want)
		}
		if err != nil && strings.Contains(err.Error(), "sk-") {
			t.Error("error leaks the key")
		}
	}
}

func TestLoadSyncDisabled(t *testing.T) {
	env := baseEnv()
	delete(env, "MEDIA_SERVER_URL")
	delete(env, "MEDIA_SERVER_TOKEN")
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.SyncEnabled() {
		t.Error("sync should be disabled")
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := []struct {
		name  string
		mod   func(map[string]string)
		extra []string
		want  string
	}{
		{"no mode", func(e map[string]string) { delete(e, "INTEGRATION_MODE") }, nil, "INTEGRATION_MODE is required"},
		{"multiple modes", func(e map[string]string) { e["INTEGRATION_MODE"] = "jellyfin,plex" }, nil, "single value"},
		{"unknown mode", func(e map[string]string) { e["INTEGRATION_MODE"] = "kodi" }, nil, "not supported"},
		{"multiple urls", func(e map[string]string) { e["MEDIA_SERVER_URL"] = "http://a:8096,http://b:8096" }, nil, "single address"},
		{"multiple urls space", func(e map[string]string) { e["MEDIA_SERVER_URL"] = "http://a:8096 http://b:8096" }, nil, "single address"},
		{"bad scheme", func(e map[string]string) { e["MEDIA_SERVER_URL"] = "file:///etc/passwd" }, nil, "http(s) URL"},
		{"url without token", func(e map[string]string) { delete(e, "MEDIA_SERVER_TOKEN") }, nil, "set together"},
		{"multiple tokens", func(e map[string]string) { e["MEDIA_SERVER_TOKEN"] = "a,b" }, nil, "MEDIA_SERVER_TOKEN must be a single"},
		{"foreign var", nil, []string{"PLEX_TOKEN=x"}, "single tenant"},
		{"other server var", nil, []string{"EMBY_URL=http://x"}, "single tenant"},
		{"state dir relative", func(e map[string]string) { e["STATE_DIR"] = "state" }, nil, "STATE_DIR must be an absolute"},
		{"state dir nested", func(e map[string]string) { e["STATE_DIR"] = "/data/downloads/.state" }, nil, "non-nested"},
		{"tiny scan interval", func(e map[string]string) { e["SCAN_INTERVAL"] = "10s" }, nil, "SCAN_INTERVAL"},
		{"no api token", func(e map[string]string) { delete(e, "API_TOKEN") }, nil, "API_TOKEN"},
		{"short api token", func(e map[string]string) { e["API_TOKEN"] = "short" }, nil, "API_TOKEN"},
		{"relative dir", func(e map[string]string) { e["MOVIES_DIR"] = "movies" }, nil, "absolute"},
		{"nested roots", func(e map[string]string) { e["TV_DIR"] = "/data/downloads/tv" }, nil, "non-nested"},
		{"same roots", func(e map[string]string) { e["TV_DIR"] = "/data/media/movies" }, nil, "non-nested"},
		{"empty video exts", func(e map[string]string) { e["VIDEO_EXTENSIONS"] = " , " }, nil, "VIDEO_EXTENSIONS"},
		{"bad ext", func(e map[string]string) { e["VIDEO_EXTENSIONS"] = "mkv,../x" }, nil, "invalid extension"},
		{"overlapping exts", func(e map[string]string) { e["SUBTITLE_EXTENSIONS"] = "srt,mkv" }, nil, "both a video and a subtitle"},
		{"multi path map", func(e map[string]string) { e["SERVER_PATH_MAP"] = "/a=/b,/c=/d" }, nil, "SERVER_PATH_MAP"},
		{"relative path map", func(e map[string]string) { e["SERVER_PATH_MAP"] = "media=/b" }, nil, "SERVER_PATH_MAP"},
		{"copy fallback not bool", func(e map[string]string) { e["COPY_FALLBACK"] = "maybe" }, nil, "COPY_FALLBACK"},
		{"bad trigger", func(e map[string]string) { e["ORGANIZE_TRIGGER"] = "cron" }, nil, "ORGANIZE_TRIGGER must be one of watch, hook"},
		{"scan interval with hook", func(e map[string]string) { e["ORGANIZE_TRIGGER"] = "hook"; e["SCAN_INTERVAL"] = "15m" }, nil, "SCAN_INTERVAL has no effect"},
		{"bad ambiguous match", func(e map[string]string) { e["AMBIGUOUS_MATCH"] = "guess" }, nil, "AMBIGUOUS_MATCH must be one of pending, best"},
		{"tiny orphan interval", func(e map[string]string) { e["ORPHAN_SCAN_INTERVAL"] = "5s" }, nil, "ORPHAN_SCAN_INTERVAL"},
		{"no state dir", func(e map[string]string) { delete(e, "STATE_DIR") }, nil, "STATE_DIR is required"},
		{"auto upgrade not bool", func(e map[string]string) { e["AUTO_UPGRADE"] = "sometimes" }, nil, "AUTO_UPGRADE"},
		{"link extras not bool", func(e map[string]string) { e["LINK_EXTRAS"] = "2" }, nil, "LINK_EXTRAS"},
		{"notify options without url", func(e map[string]string) { e["NOTIFY_ON"] = "error" }, nil, "require NOTIFY_URL"},
		{"notify bad scheme", func(e map[string]string) { e["NOTIFY_URL"] = "ftp://x/secret-topic" }, nil, "NOTIFY_URL must be"},
		{"notify bad format", func(e map[string]string) { e["NOTIFY_URL"] = "https://ntfy.sh/t"; e["NOTIFY_FORMAT"] = "xml" }, nil, "NOTIFY_FORMAT"},
		{"notify bad status", func(e map[string]string) { e["NOTIFY_URL"] = "https://ntfy.sh/t"; e["NOTIFY_ON"] = "error,exploded" }, nil, "unknown status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := baseEnv()
			if tt.mod != nil {
				tt.mod(env)
			}
			_, err := load(env, tt.extra...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret-topic") {
				t.Error("error leaks NOTIFY_URL")
			}
		})
	}
}

func TestLoadOwnServerVarsWarn(t *testing.T) {
	env := baseEnv()
	env["STATE_DIR"] = "/state"
	c, err := load(env, "JELLYFIN_PublishedServerUrl=http://x")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "JELLYFIN_PublishedServerUrl is ignored") {
		t.Errorf("warnings = %v", c.Warnings)
	}
	env["SCAN_INTERVAL"] = "15m"
	c, err = load(env)
	if err != nil || c.StateDir != "/state" || c.ScanInterval.Minutes() != 15 || len(c.Warnings) != 0 {
		t.Errorf("state/scan = %+v %v", c, err)
	}
}

func TestLoadOptions(t *testing.T) {
	env := baseEnv()
	env["AUTO_UPGRADE"] = "true"
	env["LINK_EXTRAS"] = "false"
	env["NOTIFY_URL"] = "https://apprise.local/notify/key"
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if !c.AutoUpgrade || c.LinkExtras || c.Notify.Format != "json" ||
		!c.Notify.On["conflict"] || !c.Notify.On["upgraded"] || c.Notify.On["linked"] {
		t.Errorf("config = %+v", c)
	}
	env["NOTIFY_FORMAT"] = "TEXT"
	env["NOTIFY_ON"] = "linked, error"
	c, err = load(env)
	if err != nil || c.Notify.Format != "text" || len(c.Notify.On) != 2 || !c.Notify.On["linked"] {
		t.Errorf("notify = %+v, %v", c.Notify, err)
	}
}

func TestPathMap(t *testing.T) {
	env := baseEnv()
	env["SERVER_PATH_MAP"] = "/media=/data/media"
	env["VIDEO_EXTENSIONS"] = ".MKV, mp4"
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if !c.VideoExts[".mkv"] || !c.VideoExts[".mp4"] || len(c.VideoExts) != 2 {
		t.Errorf("exts = %v", c.VideoExts)
	}
	cases := map[string]string{
		"/media/movies/A (2000)/A.mkv": "/data/media/movies/A (2000)/A.mkv",
		"/media":                       "/data/media",
		"/mediaX/a.mkv":                "/mediaX/a.mkv",
		"/other/a.mkv":                 "/other/a.mkv",
	}
	for in, want := range cases {
		if got := c.PathMap.Translate(in); got != want {
			t.Errorf("Translate(%q) = %q, want %q", in, got, want)
		}
	}
	for local, want := range map[string]string{
		"/data/media/tv/Show/Season 01": "/media/tv/Show/Season 01",
		"/data/other":                   "/data/other",
	} {
		if got := c.PathMap.ToServer(local); got != want {
			t.Errorf("ToServer(%q) = %q, want %q", local, got, want)
		}
	}
}
