// Command media-maid runs the Media Maid sidecar.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jfxdev/media-maid/internal/api"
	"github.com/jfxdev/media-maid/internal/assist"
	"github.com/jfxdev/media-maid/internal/config"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/notify"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/orphans"
	"github.com/jfxdev/media-maid/internal/safepath"
	"github.com/jfxdev/media-maid/internal/state"
	"github.com/jfxdev/media-maid/internal/watcher"
)

// version is set at build time (-ldflags "-X main.version=1.2.3").
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	log.Info("starting media-maid", "version", version)
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv, os.Environ())
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	for _, w := range cfg.Warnings {
		log.Warn("configuration", "warning", w)
	}

	mi, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("read mounts: %w", err)
	}
	vols, err := safepath.ParseMountInfo(mi)
	mi.Close()
	if err != nil {
		return err
	}
	guard, err := safepath.NewGuard(vols)
	if err != nil {
		return err
	}
	log.Info("authorized volumes", "volumes", guard.Volumes())

	downloads, err := guard.ResolveRoot("DOWNLOADS_DIR", cfg.DownloadsDir)
	if err != nil {
		return err
	}
	moviesDir, err := guard.ResolveRoot("MOVIES_DIR", cfg.MoviesDir)
	if err != nil {
		return err
	}
	tvDir, err := guard.ResolveRoot("TV_DIR", cfg.TVDir)
	if err != nil {
		return err
	}
	// STATE_DIR is created on first start; it must still lie in a mounted volume.
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("STATE_DIR: %w", err)
	}
	stateDir, err := guard.ResolveRoot("STATE_DIR", cfg.StateDir)
	if err != nil {
		return err
	}
	roots := map[string]string{"DOWNLOADS_DIR": downloads, "MOVIES_DIR": moviesDir, "TV_DIR": tvDir, "STATE_DIR": stateDir}
	if err := config.DistinctRoots(roots); err != nil {
		return err
	}
	// link(2) fails across mount points: say so now rather than on every file.
	for _, lib := range []string{"MOVIES_DIR", "TV_DIR"} {
		if guard.VolumeOf(roots[lib]) == guard.VolumeOf(downloads) {
			continue
		}
		if cfg.CopyFallback {
			log.Warn("hardlinks impossible: different mounts, files will be copied and use extra disk space", "from", "DOWNLOADS_DIR", "to", lib)
		} else {
			log.Warn("hardlinks impossible: different mounts, organizing will fail with COPY_FALLBACK=false", "from", "DOWNLOADS_DIR", "to", lib)
		}
	}
	store, err := state.Open(stateDir)
	if err != nil {
		return err
	}
	defer store.Close()
	log.Info("persistent state", "dir", stateDir)
	// Each persisted document is loaded now and saved a few seconds after it
	// changes; everything is flushed at shutdown.
	var savers []*state.Saver
	persist := func(name string, imp func([]byte) error, exp func() ([]byte, error)) func() {
		if data, err := store.Load(name); err != nil {
			log.Warn("state load failed", "document", name, "err", err)
		} else if data != nil {
			if err := imp(data); err != nil {
				log.Warn("state document ignored", "document", name, "err", err)
			}
		}
		sv := store.Saver(name, 5*time.Second, exp, log)
		savers = append(savers, sv)
		return sv.Touch
	}
	defer func() {
		for _, sv := range savers {
			sv.Flush()
		}
	}()
	moviesRoot, err := os.OpenRoot(moviesDir)
	if err != nil {
		return err
	}
	defer moviesRoot.Close()
	tvRoot, err := os.OpenRoot(tvDir)
	if err != nil {
		return err
	}
	defer tvRoot.Close()
	movies := organizer.Library{Name: "movies", Path: moviesDir, Root: moviesRoot}
	tv := organizer.Library{Name: "tv", Path: tvDir, Root: tvRoot}

	history := jobs.NewHistory(0)
	history.OnChange = persist("history", history.Import, history.Export)
	ledger := organizer.NewLedger()
	ledger.OnChange = persist("ledger", ledger.Import, ledger.Export)
	pending := organizer.NewPending()
	pending.OnChange = persist("pending", pending.Import, pending.Export)

	var notifier *notify.Notifier // nil disables notifications
	var notifyFn func(organizer.Result)
	if cfg.Notify.URL != "" {
		notifier = notify.New(notify.Options{
			URL: cfg.Notify.URL, Format: cfg.Notify.Format, On: cfg.Notify.On, Delay: 10 * time.Second, Log: log,
		})
		defer notifier.Flush()
		notifyFn = func(r organizer.Result) {
			notifier.Notify(notify.Event{Source: r.Source, Dest: r.Dest, Kind: r.Kind, Status: r.Status, Error: r.Error})
		}
		on := make([]string, 0, len(cfg.Notify.On))
		for _, st := range notify.Statuses {
			if cfg.Notify.On[st] {
				on = append(on, st)
			}
		}
		// The URL may carry a token: only the format and statuses are logged.
		log.Info("notifications enabled", "format", cfg.Notify.Format, "on", on)
	}

	var client *integration.Client
	if cfg.SyncEnabled() {
		client, err = integration.NewClient(cfg.Platform, cfg.ServerURL, cfg.ServerToken)
		if err != nil {
			return err
		}
		host := cfg.ServerURL
		if i := strings.Index(host, "://"); i >= 0 {
			host = host[i+3:]
		}
		log.Info("media server integration", "mode", cfg.Mode, "host", host)
	} else {
		log.Info("media server integration", "mode", cfg.Mode, "sync", "disabled")
	}
	syncer := integration.NewSyncer(client, cfg.SyncDebounce, history, log)
	syncer.ToServer = cfg.PathMap.ToServer
	defer syncer.Stop()

	var meta metadata.Provider // nil disables the TMDB layer
	if cfg.TMDBKey != "" {
		tmdb := metadata.NewTMDB(cfg.TMDBKey, "")
		tmdb.OnChange = persist("tmdb-cache", tmdb.Import, tmdb.Export)
		meta = tmdb
		log.Info("tmdb enrichment enabled")
	}

	var resolver assist.Resolver // nil disables the LLM layer
	if cfg.LLM.Provider != "" {
		c, err := assist.New(assist.Options{
			Provider: cfg.LLM.Provider, BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
			Timeout: cfg.LLM.Timeout, MinConfidence: cfg.LLM.MinConfidence, Log: log,
		})
		if err != nil {
			return err
		}
		c.OnChange = persist("llm-cache", c.Import, c.Export)
		resolver = c
		log.Info("llm decision layer enabled", "provider", cfg.LLM.Provider, "model", c.Model(), "min_confidence", cfg.LLM.MinConfidence)
	}
	if cfg.PreferredLanguage != "" {
		log.Info("localized titles", "language", cfg.PreferredLanguage)
	}

	filter := organizer.Filter{Video: cfg.VideoExts, Subtitle: cfg.SubtitleExts}
	org := organizer.New(organizer.Options{
		DownloadsDir: downloads, Movies: movies, TV: tv, Filter: filter, Guard: guard,
		Meta: meta, Assist: resolver, MinConfidence: cfg.LLM.MinConfidence, Language: cfg.PreferredLanguage,
		Platform: cfg.Platform, Syncer: syncer, History: history, Log: log, DryRun: cfg.DryRun,
		Ledger: ledger, NoCopy: !cfg.CopyFallback, Pending: pending, Notify: notifyFn,
		AutoUpgrade: cfg.AutoUpgrade, LinkExtras: cfg.LinkExtras,
	})
	log.Info("organizer", "auto_upgrade", cfg.AutoUpgrade, "link_extras", cfg.LinkExtras, "copy_fallback", cfg.CopyFallback)

	var server orphans.Server
	if client != nil {
		server = client
	}
	det := orphans.New(orphans.Options{
		Server: server, PathMap: cfg.PathMap, Libraries: []organizer.Library{movies, tv},
		Filter: filter, Guard: guard, History: history, Log: log, DryRun: cfg.DryRun,
		Downloads: downloads, Ledger: ledger, Exclusive: org.Exclusive,
	})
	det.OnChange = persist("orphans", det.Import, det.Export)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w, err := watcher.New(downloads, cfg.StableDuration, func(ctx context.Context, p string) {
		_, err := org.Organize(ctx, p, organizer.OrganizeOptions{})
		// Not found and unauthorized links are already recorded by the organizer.
		if err != nil && !errors.Is(err, organizer.ErrNotFound) && !errors.Is(err, safepath.ErrUnauthorizedVolume) {
			log.Warn("organize failed", "err", err)
		}
	}, log)
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}
	go w.Run(ctx)

	if cfg.ScanInterval > 0 {
		// Fallback for missed filesystem events (network filesystems). Files
		// still changing within STABLE_SECONDS are left for the next run.
		go func() {
			t := time.NewTicker(cfg.ScanInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if _, err := org.Organize(ctx, downloads, organizer.OrganizeOptions{MinAge: cfg.StableDuration}); err != nil && ctx.Err() == nil {
						log.Warn("periodic scan failed", "err", err)
					}
				}
			}
		}()
	}

	if cfg.OrphanScanInterval > 0 {
		go func() {
			t := time.NewTicker(cfg.OrphanScanInterval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					_, _ = det.Scan(ctx)
				}
			}
		}()
	}

	apiServer := api.New(cfg.APIToken, org, det, syncer, history, log)
	apiServer.SetContext(ctx)
	apiServer.SetHealth(w.Healthy)
	srv := api.NewHTTPServer(cfg.ListenAddr, apiServer.Handler())
	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr, "dry_run", cfg.DryRun)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
