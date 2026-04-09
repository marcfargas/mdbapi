package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/marcfargas/mdbapi/internal/config"
	"github.com/marcfargas/mdbapi/internal/mdb"
)

// mockRegistrar records Register calls.
type mockRegistrar struct {
	mu         sync.Mutex
	aliases    []string
	registered []mdb.DBConfig
}

func (m *mockRegistrar) Aliases() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.aliases...)
}

func (m *mockRegistrar) Register(cfg mdb.DBConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aliases = append(m.aliases, cfg.Alias)
	m.registered = append(m.registered, cfg)
	return nil
}

func (m *mockRegistrar) Registered() []mdb.DBConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mdb.DBConfig{}, m.registered...)
}

func TestWatcher_PollDetectsNewFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)

	globPattern := filepath.Join(dir, "**", "*.mdb")
	globs := []config.DatabaseConfig{{Glob: globPattern}}
	reg := &mockRegistrar{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(globs, reg, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run(ctx)

	// Drop a new file — watcher should pick it up.
	time.Sleep(50 * time.Millisecond) // let watcher start
	newFile := filepath.Join(sub, "newdb.mdb")
	os.WriteFile(newFile, []byte("fake"), 0644)

	// Wait for poll to detect it.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for watcher to register new database")
		default:
		}
		got := reg.Registered()
		if len(got) >= 1 {
			if got[0].Alias != "sub_newdb" {
				t.Errorf("alias = %q, want %q", got[0].Alias, "sub_newdb")
			}
			if got[0].Path != newFile {
				t.Errorf("path = %q, want %q", got[0].Path, newFile)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWatcher_DeduplicatesExistingAliases(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "existing.mdb"), nil, 0644)

	globPattern := filepath.Join(dir, "**", "*.mdb")
	globs := []config.DatabaseConfig{{Glob: globPattern}}
	// Pre-seed the alias so watcher skips it.
	reg := &mockRegistrar{aliases: []string{"sub_existing"}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := New(globs, reg, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go w.Run(ctx)

	// Let a couple poll cycles run.
	time.Sleep(350 * time.Millisecond)
	cancel()

	if got := reg.Registered(); len(got) != 0 {
		t.Errorf("expected 0 registrations for pre-existing alias, got %d", len(got))
	}
}

func TestWatcher_PollBackoff_IncreasesWhenIdle(t *testing.T) {
	dir := t.TempDir()
	// Empty dir — poll will find nothing.
	globPattern := filepath.Join(dir, "**", "*.mdb")
	globs := []config.DatabaseConfig{{Glob: globPattern}}
	reg := &mockRegistrar{}

	w, err := New(globs, reg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// After a few idle poll cycles, interval should have grown.
	time.Sleep(300 * time.Millisecond)
	cancel()

	if w.PollInterval() <= 50*time.Millisecond {
		t.Errorf("expected poll interval to back off, got currPollIvl=%v basePollIvl=%v", w.PollInterval(), 50*time.Millisecond)
	}
}

func TestWatcher_PollBackoff_ResetsOnDiscovery(t *testing.T) {
	dir := t.TempDir()
	globPattern := filepath.Join(dir, "**", "*.mdb")
	globs := []config.DatabaseConfig{{Glob: globPattern}}
	reg := &mockRegistrar{}

	w, err := New(globs, reg, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Let it back off with empty dir.
	time.Sleep(200 * time.Millisecond)
	if w.PollInterval() <= 50*time.Millisecond {
		t.Fatal("expected backoff before adding file")
	}

	// Drop a file — next poll should find it and reset interval.
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)
	os.WriteFile(filepath.Join(sub, "found.mdb"), nil, 0644)

	// Wait for discovery, then immediately check that the interval was reset.
	// The reset happens in the same poll tick that discovers the file.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for discovery")
		default:
		}
		if len(reg.Registered()) >= 1 {
			// Check immediately — the poll that found the file also reset the interval.
			if w.PollInterval() != 50*time.Millisecond {
				t.Errorf("expected poll interval to reset to base after discovery, got %v", w.PollInterval())
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWatcher_DebounceCoalescesDuplicateEvents(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0755)

	dbPath := filepath.Join(sub, "test.mdb")
	os.WriteFile(dbPath, nil, 0644)

	globPattern := filepath.Join(dir, "**", "*.mdb")
	globs := []config.DatabaseConfig{{Glob: globPattern}}
	reg := &mockRegistrar{}

	w, err := New(globs, reg, 10*time.Minute) // Very long poll so only fsnotify triggers.
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.fsw == nil {
		t.Skip("fsnotify not available")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(50 * time.Millisecond) // let watcher start

	// Simulate multiple rapid Create events for the same file.
	// We write to a second file in the watched dir to trigger fsnotify Create events.
	// The debouncer should coalesce them.
	for i := 0; i < 5; i++ {
		dup := filepath.Join(sub, "dup.mdb")
		os.WriteFile(dup, []byte{byte(i)}, 0644)
		os.Remove(dup)
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for debounce flush (quiet=500ms + margin).
	time.Sleep(3 * time.Second)
	cancel()

	// Should register at most the files that exist — deduplication prevents repeats.
	got := reg.Registered()
	// test.mdb should be registered at most once (via poll or fsnotify).
	count := 0
	for _, r := range got {
		if r.Alias == "sub_test" {
			count++
		}
	}
	if count > 1 {
		t.Errorf("expected at most 1 registration for sub_test, got %d", count)
	}
}
