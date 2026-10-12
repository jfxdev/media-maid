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
	if !c.VideoExts[".mkv"] || !c.SubtitleExts[".srt"] || c.SyncDebounce.Seconds() != 30 {
		t.Errorf("defaults not applied")
	}
	for _, mode := range []string{"emby", "plex", "PLEX"} {
		env := baseEnv()
		env["INTEGRATION_MODE"] = mode
		if _, err := load(env); err != nil {
			t.Errorf("mode %s: %v", mode, err)
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
		{"legacy jellyfin var", nil, []string{"JELLYFIN_URL=http://x"}, "single tenant"},
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
		{"tiny orphan interval", func(e map[string]string) { e["ORPHAN_SCAN_INTERVAL"] = "5s" }, nil, "ORPHAN_SCAN_INTERVAL"},
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
		})
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
}
