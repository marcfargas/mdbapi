package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// makeTempDB creates an empty file at the given path, creating parent dirs.
func makeTempDB(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// ─── aliasFromPath ──────────────────────────────────────────────────────────

func TestAliasFromPath_Flat(t *testing.T) {
	base := filepath.Join("C:", "data")
	path := filepath.Join(base, "Invoices.mdb")
	got, err := aliasFromPath(base, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "invoices" {
		t.Errorf("got %q, want %q", got, "invoices")
	}
}

func TestAliasFromPath_Nested(t *testing.T) {
	base := filepath.Join("C:", "data")
	path := filepath.Join(base, "2024", "Orders.mdb")
	got, err := aliasFromPath(base, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "2024_orders" {
		t.Errorf("got %q, want %q", got, "2024_orders")
	}
}

func TestAliasFromPath_SpecialChars(t *testing.T) {
	base := filepath.Join("C:", "data")
	path := filepath.Join(base, "My DB (v2).mdb")
	got, err := aliasFromPath(base, path)
	if err != nil {
		t.Fatal(err)
	}
	// spaces and parens replaced with _
	if !strings.HasPrefix(got, "my_db") {
		t.Errorf("got %q, expected prefix %q", got, "my_db")
	}
}

// ─── globBase ───────────────────────────────────────────────────────────────

func TestGlobBase(t *testing.T) {
	tests := []struct {
		pattern string
		want    string
	}{
		{filepath.Join("C:", "data", "*.mdb"), filepath.Join("C:", "data")},
		{filepath.Join("C:", "data", "**", "*.mdb"), filepath.Join("C:", "data")},
		{filepath.Join("C:", "data", "sub", "*.mdb"), filepath.Join("C:", "data", "sub")},
	}
	for _, tc := range tests {
		got := globBase(tc.pattern)
		if got != tc.want {
			t.Errorf("globBase(%q) = %q, want %q", tc.pattern, got, tc.want)
		}
	}
}

// ─── expandGlobs ────────────────────────────────────────────────────────────

func TestExpandGlobs_Explicit(t *testing.T) {
	in := []DatabaseConfig{
		{Alias: "a", Path: `C:\data\a.mdb`},
		{Alias: "b", Path: `C:\data\b.mdb`},
	}
	out, err := expandGlobs(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2", len(out))
	}
}

func TestExpandGlobs_ExplicitDedup(t *testing.T) {
	in := []DatabaseConfig{
		{Alias: "db", Path: `C:\a.mdb`},
		{Alias: "DB", Path: `C:\b.mdb`}, // same alias, different case
	}
	out, err := expandGlobs(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1 (dedup)", len(out))
	}
	if out[0].Path != `C:\a.mdb` {
		t.Errorf("first entry should win, got path %q", out[0].Path)
	}
}

func TestExpandGlobs_SingleLevel(t *testing.T) {
	dir := t.TempDir()
	makeTempDB(t, filepath.Join(dir, "alpha.mdb"))
	makeTempDB(t, filepath.Join(dir, "beta.accdb"))
	makeTempDB(t, filepath.Join(dir, "readme.txt")) // should not match

	in := []DatabaseConfig{
		{Glob: filepath.Join(dir, "*.mdb")},
	}
	out, err := expandGlobs(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1 (only .mdb)", len(out))
	}
	if out[0].Alias != "alpha" {
		t.Errorf("alias = %q, want %q", out[0].Alias, "alpha")
	}
}

func TestExpandGlobs_Recursive(t *testing.T) {
	dir := t.TempDir()
	makeTempDB(t, filepath.Join(dir, "root.mdb"))               // in base — must NOT match **
	makeTempDB(t, filepath.Join(dir, "sub", "child.mdb"))        // subdirectory — must match
	makeTempDB(t, filepath.Join(dir, "sub", "deep", "leaf.mdb")) // nested subdirectory — must match

	in := []DatabaseConfig{
		{Glob: filepath.Join(dir, "**", "*.mdb")},
	}
	out, err := expandGlobs(in)
	if err != nil {
		t.Fatal(err)
	}
	// ** matches subdirectories only, NOT files directly in the base.
	// root.mdb should NOT be included.
	if len(out) != 2 {
		names := make([]string, len(out))
		for i, e := range out {
			names[i] = e.Alias
		}
		t.Fatalf("got %d entries %v, want 2 (sub_child, sub_deep_leaf)", len(out), names)
	}
	aliases := make([]string, len(out))
	for i, e := range out {
		aliases[i] = e.Alias
	}
	sort.Strings(aliases)
	want := []string{"sub_child", "sub_deep_leaf"}
	for i, a := range aliases {
		if a != want[i] {
			t.Errorf("aliases[%d] = %q, want %q", i, a, want[i])
		}
	}
}

func TestExpandGlobs_RecursiveDoesNotMatchBase(t *testing.T) {
	dir := t.TempDir()
	makeTempDB(t, filepath.Join(dir, "base.mdb")) // only file, directly in base

	in := []DatabaseConfig{
		{Glob: filepath.Join(dir, "**", "*.mdb")},
	}
	out, err := expandGlobs(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Errorf("** should not match files in base dir, got %d entries", len(out))
	}
}

func TestExpandGlobs_NoMatch(t *testing.T) {
	dir := t.TempDir() // empty directory
	in := []DatabaseConfig{
		{Glob: filepath.Join(dir, "*.mdb")},
	}
	out, err := expandGlobs(in)
	if err != nil {
		t.Fatal(err)
	}
	// No match is a warning, not an error; result is empty.
	if len(out) != 0 {
		t.Errorf("got %d entries for empty dir, want 0", len(out))
	}
}

func TestExpandGlobs_CollisionAcrossGlobs(t *testing.T) {
	dir := t.TempDir()
	makeTempDB(t, filepath.Join(dir, "a", "sales.mdb"))
	makeTempDB(t, filepath.Join(dir, "b", "sales.mdb")) // same leaf name → same alias

	in := []DatabaseConfig{
		{Glob: filepath.Join(dir, "a", "*.mdb")},
		{Glob: filepath.Join(dir, "b", "*.mdb")},
	}
	out, err := expandGlobs(in)
	if err != nil {
		t.Fatal(err)
	}
	// Second "sales" is a collision and should be skipped.
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1 (collision dedup)", len(out))
	}
}

// ─── Load() integration ──────────────────────────────────────────────────────

func TestLoad_GlobEntry(t *testing.T) {
	dir := t.TempDir()
	makeTempDB(t, filepath.Join(dir, "invoices.mdb"))
	makeTempDB(t, filepath.Join(dir, "orders.mdb"))

	key := strings.Repeat("a", 32)
	content := "auth:\n  keys:\n    - \"" + key + "\"\ndatabases:\n  - glob: \"" +
		filepath.ToSlash(filepath.Join(dir, "*.mdb")) + "\"\n"

	path := writeTemp(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Databases) != 2 {
		t.Errorf("got %d databases after glob expansion, want 2", len(cfg.Databases))
	}
	for _, db := range cfg.Databases {
		if db.Glob != "" {
			t.Errorf("post-Load database should have empty Glob, got %q", db.Glob)
		}
		if db.Alias == "" {
			t.Error("post-Load database should have non-empty Alias")
		}
	}
}

func TestLoad_GlobAndExplicitMixed(t *testing.T) {
	dir := t.TempDir()
	makeTempDB(t, filepath.Join(dir, "extra.mdb"))

	key := strings.Repeat("a", 32)
	content := "auth:\n  keys:\n    - \"" + key + "\"\ndatabases:\n" +
		"  - alias: named\n    path: 'C:\\dummy.mdb'\n" +
		"  - glob: \"" + filepath.ToSlash(filepath.Join(dir, "*.mdb")) + "\"\n"

	path := writeTemp(t, content)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// 1 explicit + 1 from glob = 2
	if len(cfg.Databases) != 2 {
		t.Errorf("got %d databases, want 2", len(cfg.Databases))
	}
}

func TestLoad_GlobWithAliasFails(t *testing.T) {
	key := strings.Repeat("a", 32)
	content := "auth:\n  keys:\n    - \"" + key + "\"\ndatabases:\n" +
		"  - glob: 'C:\\*.mdb'\n    alias: bad\n"
	path := writeTemp(t, content)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected validation error for glob+alias, got nil")
	}
	if !strings.Contains(err.Error(), "cannot set both") {
		t.Errorf("error should mention conflict, got: %v", err)
	}
}
