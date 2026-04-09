package config

import (
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// expandGlobs resolves DatabaseConfig entries that carry a Glob field into
// concrete Alias+Path entries. Explicit (non-glob) entries pass through
// unchanged.
//
// Alias derivation for glob matches:
//
//	base = longest prefix of the pattern before the first wildcard
//	alias = filepath.Rel(base, match), extension stripped,
//	        path separators → "_", lowercased, non-alphanumeric → "_"
//
// Example:
//
//	glob  C:\data\*.mdb      + match C:\data\Invoices.mdb     → alias "invoices"
//	glob  C:\data\**\*.mdb   + match C:\data\2024\Orders.mdb  → alias "2024_orders"
//
// Alias collisions: the first entry wins; duplicates are logged and skipped.
// A glob that matches no files logs a warning but is not an error.
func expandGlobs(entries []DatabaseConfig) ([]DatabaseConfig, error) {
	seen := make(map[string]string) // lower(alias) → path already registered
	var result []DatabaseConfig

	for _, entry := range entries {
		if entry.Glob == "" {
			// Explicit entry — pass through, dedup by alias.
			key := strings.ToLower(entry.Alias)
			if src, exists := seen[key]; exists {
				log.Printf("config: alias %q collision: %q vs %q; skipping duplicate", entry.Alias, src, entry.Path)
				continue
			}
			seen[key] = entry.Path
			result = append(result, entry)
			continue
		}

		// Glob entry — expand to concrete paths.
		matches, err := MatchGlob(entry.Glob)
		if err != nil {
			return nil, fmt.Errorf("glob %q: %w", entry.Glob, err)
		}
		if len(matches) == 0 {
			log.Printf("config: glob %q matched no files", entry.Glob)
			continue
		}

		base := GlobBase(entry.Glob)
		for _, path := range matches {
			alias, err := AliasFromPath(base, path)
			if err != nil {
				return nil, fmt.Errorf("glob %q: derive alias for %q: %w", entry.Glob, path, err)
			}
			key := strings.ToLower(alias)
			if src, exists := seen[key]; exists {
				log.Printf("config: alias %q collision (glob %q): %q vs %q; skipping", alias, entry.Glob, src, path)
				continue
			}
			seen[key] = path
			result = append(result, DatabaseConfig{Alias: alias, Path: path})
		}
	}

	return result, nil
}

// MatchGlob returns all paths matching pattern.
// Supports both single-level (*) and recursive (**) wildcards.
// Path separators are normalised to filepath.Separator before matching.
func MatchGlob(pattern string) ([]string, error) {
	pattern = filepath.Clean(pattern)

	if !strings.Contains(pattern, "**") {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			log.Printf("config: filepath.Glob(%q) error: %v", pattern, err)
		} else if len(matches) == 0 {
			// Log the base directory contents to aid debugging on mapped drives.
			dir := filepath.Dir(pattern)
			entries, dirErr := os.ReadDir(dir)
			if dirErr != nil {
				log.Printf("config: glob %q matched 0 files; cannot read dir %q: %v", pattern, dir, dirErr)
			} else {
				log.Printf("config: glob %q matched 0 files; dir %q has %d entries", pattern, dir, len(entries))
			}
		}
		return matches, err
	}

	// Recursive match: walk from base directory, match the leaf name pattern.
	// ** matches files in subdirectories only, not the base directory itself.
	// Use base\*.mdb (single *) to match files directly in the base.
	base := GlobBase(pattern)
	leafPattern := strings.ToLower(filepath.Base(pattern)) // e.g. "*.mdb"
	absBase, _ := filepath.Abs(base)

	var matches []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Permission errors etc. — skip silently.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// ** means subdirectories only — skip files directly in base.
		absDir, _ := filepath.Abs(filepath.Dir(path))
		if strings.EqualFold(absDir, absBase) {
			return nil
		}
		ok, merr := filepath.Match(leafPattern, strings.ToLower(d.Name()))
		if merr != nil {
			return merr
		}
		if ok {
			matches = append(matches, path)
		}
		return nil
	})
	return matches, err
}

// PathMatchesGlob checks whether a single path matches a glob pattern without
// walking the filesystem. This is O(1) — no I/O, no directory listing.
// For ** patterns: verifies path is under base (not IN base) and leaf matches.
// For simple patterns: uses filepath.Match after cleaning.
func PathMatchesGlob(pattern, path string) bool {
	pattern = filepath.Clean(pattern)
	path = filepath.Clean(path)

	if !strings.Contains(pattern, "**") {
		ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(path))
		return err == nil && ok
	}

	// Recursive pattern: base\**\leafPattern
	base := GlobBase(pattern)
	leafPattern := strings.ToLower(filepath.Base(pattern)) // e.g. "*.mdb"

	absBase, _ := filepath.Abs(base)
	absPath, _ := filepath.Abs(path)
	absDir, _ := filepath.Abs(filepath.Dir(path))

	// Must be under base, not directly in base (** = subdirectories only).
	if strings.EqualFold(absDir, absBase) {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(absPath), strings.ToLower(absBase)+strings.ToLower(string(filepath.Separator))) {
		return false
	}

	ok, err := filepath.Match(leafPattern, strings.ToLower(filepath.Base(path)))
	return err == nil && ok
}

// GlobBase returns the longest path prefix before the first wildcard (* or ?).
//
//	C:\data\*.mdb       → C:\data
//	C:\data\**\*.mdb    → C:\data
//	C:\data\sub\*.mdb   → C:\data\sub
func GlobBase(pattern string) string {
	pattern = filepath.Clean(pattern)
	vol := filepath.VolumeName(pattern) // "C:" on Windows, "" on Unix
	rest := pattern[len(vol):]

	parts := strings.Split(rest, string(filepath.Separator))
	var safe []string
	for _, p := range parts {
		if strings.ContainsAny(p, "*?") {
			break
		}
		safe = append(safe, p)
	}
	if len(safe) == 0 {
		return vol + string(filepath.Separator)
	}
	joined := vol + strings.Join(safe, string(filepath.Separator))
	// Preserve a root separator when safe starts with an empty string (Unix "/foo").
	if len(rest) > 0 && os.IsPathSeparator(rest[0]) && !strings.HasPrefix(joined, vol+string(filepath.Separator)) {
		joined = vol + string(filepath.Separator) + strings.TrimPrefix(joined, vol)
	}
	return joined
}

// AliasFromPath derives a URL-safe, lowercase alias from a matched path.
//
//	base  = C:\data
//	path  = C:\data\2024\Orders.mdb
//	→ rel = 2024\Orders.mdb  →  "2024_orders"
func AliasFromPath(base, path string) (string, error) {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return "", err
	}
	// Strip extension, replace separators with underscores.
	noExt := strings.TrimSuffix(rel, filepath.Ext(rel))
	raw := strings.ReplaceAll(noExt, string(filepath.Separator), "_")
	raw = strings.ToLower(raw)

	// Keep only [a-z0-9_-]; replace everything else with _.
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String(), nil
}
