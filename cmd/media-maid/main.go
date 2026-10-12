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
	"github.com/jfxdev/media-maid/internal/config"
	"github.com/jfxdev/media-maid/internal/integration"
	"github.com/jfxdev/media-maid/internal/jobs"
	"github.com/jfxdev/media-maid/internal/metadata"
	"github.com/jfxdev/media-maid/internal/organizer"
	"github.com/jfxdev/media-maid/internal/orphans"
	"github.com/jfxdev/media-maid/internal/safepath"
	"github.com/jfxdev/media-maid/internal/watcher"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
	if err := config.DistinctRoots(map[string]string{"DOWNLOADS_DIR": downloads, "MOVIES_DIR": moviesDir, "TV_DIR": tvDir}); err != nil {
		return err
	}
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
	defer syncer.Stop()

	var meta metadata.Provider = metadata.Noop{}
	if cfg.TMDBKey != "" {
		meta = metadata.NewTMDB(cfg.TMDBKey, "")
		log.Info("tmdb enrichment enabled")
	}

	filter := organizer.Filter{Video: cfg.VideoExts, Subtitle: cfg.SubtitleExts}
	org := organizer.New(organizer.Options{
		DownloadsDir: downloads, Movies: movies, TV: tv, Filter: filter, Guard: guard,
		Meta: meta, Platform: cfg.Platform, Syncer: syncer, History: history, Log: log, DryRun: cfg.DryRun,
	})

	var server orphans.Server
	if client != nil {
		server = client
	}
	det := orphans.New(orphans.Options{
		Server: server, PathMap: cfg.PathMap, Libraries: []organizer.Library{movies, tv},
		Filter: filter, Guard: guard, History: history, Log: log, DryRun: cfg.DryRun,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	w, err := watcher.New(downloads, cfg.StableDuration, func(ctx context.Context, p string) {
		_, err := org.Organize(ctx, p, false)
		// Not found and unauthorized links are already recorded by the organizer.
		if err != nil && !errors.Is(err, organizer.ErrNotFound) && !errors.Is(err, safepath.ErrUnauthorizedVolume) {
			log.Warn("organize failed", "err", err)
		}
	}, log)
	if err != nil {
		return fmt.Errorf("watcher: %w", err)
	}
	go w.Run(ctx)

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

	srv := api.NewHTTPServer(cfg.ListenAddr, api.New(cfg.APIToken, org, det, syncer, history, log).Handler())
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
