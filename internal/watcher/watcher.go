// Package watcher monitors filesystem for new databases matching configured globs.
package watcher

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
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

const (
	// debounceQuiet is how long to wait after the last event before flushing.
	debounceQuiet = 500 * time.Millisecond
	// debounceMax is the maximum time to buffer events before forcing a flush.
	debounceMax = 2 * time.Second
	// maxPollBackoff is the upper bound for adaptive poll interval.
	maxPollBackoff = 5 * time.Minute
)

// Watcher watches for new database files matching configured glob patterns.
type Watcher struct {
	globs       []globEntry
	reg         Registrar
	basePollIvl time.Duration
	mu          sync.Mutex     // protects currPollIvl
	currPollIvl time.Duration
	fsw         *fsnotify.Watcher
}

// PollInterval returns the current poll interval (thread-safe).
func (w *Watcher) PollInterval() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.currPollIvl
}

// New creates a Watcher. Pass only the glob-type DatabaseConfig entries.
// pollInterval controls the base (minimum) poll frequency; it backs off when idle.
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
		return &Watcher{globs: entries, reg: reg, basePollIvl: pollInterval, currPollIvl: pollInterval}, nil
	}

	// Watch base directories for each glob.
	for _, e := range entries {
		if err := fsw.Add(e.base); err != nil {
			slog.Warn("fsnotify: cannot watch directory, relying on poll", "dir", e.base, "err", err)
		}
	}

	return &Watcher{globs: entries, reg: reg, basePollIvl: pollInterval, currPollIvl: pollInterval, fsw: fsw}, nil
}

// Run starts the watcher. Blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.currPollIvl)
	defer ticker.Stop()
	if w.fsw != nil {
		defer w.fsw.Close()
	}

	// fsnotify event loop with debouncing (if available).
	if w.fsw != nil {
		go w.fsnotifyLoop(ctx)
	}

	// Poll loop with adaptive backoff.
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			found := w.poll()
			w.mu.Lock()
			if found > 0 {
				w.currPollIvl = w.basePollIvl
			} else if w.currPollIvl < maxPollBackoff {
				w.currPollIvl *= 2
				if w.currPollIvl > maxPollBackoff {
					w.currPollIvl = maxPollBackoff
				}
			}
			ticker.Reset(w.currPollIvl)
			w.mu.Unlock()
		}
	}
}

// fsnotifyLoop collects Create events into a debounce buffer, flushing after
// 500ms of quiet or 2s max wait — whichever comes first.
func (w *Watcher) fsnotifyLoop(ctx context.Context) {
	pending := make(map[string]struct{})
	var quietTimer *time.Timer
	var maxTimer *time.Timer

	quietCh := func() <-chan time.Time {
		if quietTimer == nil {
			return nil
		}
		return quietTimer.C
	}
	maxCh := func() <-chan time.Time {
		if maxTimer == nil {
			return nil
		}
		return maxTimer.C
	}

	flush := func() {
		for path := range pending {
			w.tryRegister(path, "fsnotify")
		}
		pending = make(map[string]struct{})
		if quietTimer != nil {
			quietTimer.Stop()
			quietTimer = nil
		}
		if maxTimer != nil {
			maxTimer.Stop()
			maxTimer = nil
		}
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return

		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if event.Op&fsnotify.Create == 0 {
				continue
			}
			// Quick filter: only buffer .mdb/.accdb files.
			ext := strings.ToLower(filepath.Ext(event.Name))
			if ext != ".mdb" && ext != ".accdb" {
				continue
			}
			pending[filepath.Clean(event.Name)] = struct{}{}

			// Reset quiet timer on each event.
			if quietTimer == nil {
				quietTimer = time.NewTimer(debounceQuiet)
			} else {
				quietTimer.Reset(debounceQuiet)
			}
			// Start max timer on first event of a batch.
			if maxTimer == nil {
				maxTimer = time.NewTimer(debounceMax)
			}

		case <-quietCh():
			flush()

		case <-maxCh():
			flush()

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			slog.Warn("fsnotify error", "err", err)
		}
	}
}

// poll re-expands all glob patterns and registers any new matches.
// Returns the number of newly registered databases.
func (w *Watcher) poll() int {
	found := 0
	for _, g := range w.globs {
		matches, err := config.MatchGlob(g.pattern)
		if err != nil {
			slog.Warn("poll: glob error", "pattern", g.pattern, "err", err)
			continue
		}
		for _, path := range matches {
			if w.tryRegister(path, "poll") {
				found++
			}
		}
	}
	return found
}

// tryRegister checks if a file matches any glob and registers it if new.
// Returns true if a new database was registered.
func (w *Watcher) tryRegister(path string, source string) bool {
	path = filepath.Clean(path)

	// Check extension — must be .mdb or .accdb.
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".mdb" && ext != ".accdb" {
		return false
	}

	// Find which glob this path matches and derive alias (O(1) per glob, no tree walk).
	for _, g := range w.globs {
		if !config.PathMatchesGlob(g.pattern, path) {
			continue
		}
		alias, err := config.AliasFromPath(g.base, path)
		if err != nil {
			slog.Warn("watcher: alias derivation failed", "path", path, "err", err)
			return false
		}

		// Check if already registered.
		for _, existing := range w.reg.Aliases() {
			if strings.EqualFold(existing, alias) {
				return false
			}
		}

		if err := w.reg.Register(mdb.DBConfig{Alias: alias, Path: path}); err != nil {
			slog.Warn("watcher: register failed", "alias", alias, "path", path, "err", err)
			return false
		}
		slog.Info("database added", "alias", alias, "path", path, "source", source)
		return true
	}
	return false
}
