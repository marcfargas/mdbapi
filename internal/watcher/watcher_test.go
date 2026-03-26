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
