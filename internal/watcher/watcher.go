// Package watcher monitors filesystem for new databases matching configured globs.
package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/marcfargas/mdbapi/internal/config"
	"github.com/marcfargas/mdbapi/internal/mdb"
)

// Registrar is the subset of mdb.Pool used by the watcher.
type Registrar interface {
	Aliases() []string
	Register(cfg mdb.DBConfig) error
}

// globEntry holds pre-computed fields for a single glob pattern.
type globEntry struct {
	pattern string // original glob pattern
	base    string // longest non-wildcard prefix (directory to watch)
}

// Watcher watches for new database files matching configured glob patterns.
type Watcher struct {
	globs   []globEntry
	reg     Registrar
	pollIvl time.Duration
	fsw     *fsnotify.Watcher
}

// New creates a Watcher. Pass only the glob-type DatabaseConfig entries.
// pollInterval controls how often the poll fallback re-expands globs.
func New(globs []config.DatabaseConfig, reg Registrar, pollInterval time.Duration) (*Watcher, error) {
	var entries []globEntry
	for _, g := range globs {
		if g.Glob == "" {
			continue
		}
		entries = append(entries, globEntry{
			pattern: g.Glob,
			base:    config.GlobBase(g.Glob),
		})
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Warn("fsnotify unavailable, relying on poll only", "err", err)
		return &Watcher{globs: entries, reg: reg, pollIvl: pollInterval}, nil
	}

	// Watch base directories for each glob.
	for _, e := range entries {
		if err := fsw.Add(e.base); err != nil {
			slog.Warn("fsnotify: cannot watch directory, relying on poll", "dir", e.base, "err", err)
		}
	}

	return &Watcher{globs: entries, reg: reg, pollIvl: pollInterval, fsw: fsw}, nil
}

// Run starts the watcher. Blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollIvl)
	defer ticker.Stop()
	if w.fsw != nil {
		defer w.fsw.Close()
	}

	// fsnotify event loop (if available).
	if w.fsw != nil {
		go w.fsnotifyLoop(ctx)
	}

	// Poll loop.
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll()
		}
	}
}

// fsnotifyLoop handles filesystem events.
func (w *Watcher) fsnotifyLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == 0 {
				continue
			}
			w.tryRegister(event.Name, "fsnotify")
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			slog.Warn("fsnotify error", "err", err)
		}
	}
}

// poll re-expands all glob patterns and registers any new matches.
func (w *Watcher) poll() {
	for _, g := range w.globs {
		matches, err := config.MatchGlob(g.pattern)
		if err != nil {
			slog.Warn("poll: glob error", "pattern", g.pattern, "err", err)
			continue
		}
		for _, path := range matches {
			w.tryRegister(path, "poll")
		}
	}
}

// tryRegister checks if a file matches any glob and registers it if new.
func (w *Watcher) tryRegister(path string, source string) {
	path = filepath.Clean(path)

	// Check extension — must be .mdb or .accdb.
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".mdb" && ext != ".accdb" {
		return
	}

	// Find which glob this path matches and derive alias.
	for _, g := range w.globs {
		matches, err := config.MatchGlob(g.pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if filepath.Clean(m) != path {
				continue
			}
			alias, err := config.AliasFromPath(g.base, path)
			if err != nil {
				slog.Warn("watcher: alias derivation failed", "path", path, "err", err)
				return
			}

			// Check if already registered.
			for _, existing := range w.reg.Aliases() {
				if strings.EqualFold(existing, alias) {
					return
				}
			}

			if err := w.reg.Register(mdb.DBConfig{Alias: alias, Path: path}); err != nil {
				slog.Warn("watcher: register failed", "alias", alias, "path", path, "err", err)
				return
			}
			slog.Info("database added", "alias", alias, "path", path, "source", source)
			return
		}
	}
}
