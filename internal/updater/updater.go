// Package updater checks for new releases on GitHub and applies them.
// Supports two channels:
//   - "release": downloads from GitHub Releases (default, uses go-selfupdate)
//   - "develop": downloads latest CI artifact from the develop branch
//
// The update flow is two-phase: download+verify, then signal for restart.
// The actual restart is handled by Windows SCM recovery actions (exit code 1).
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// Updater polls GitHub releases or CI artifacts and signals when an update
// has been applied.
type Updater struct {
	repo          string
	currentVer    string
	checkInterval time.Duration
	token         string
	channel       string // "release" or "develop"
	restartSignal chan<- struct{}
}

// New creates an Updater. restartSignal is closed when an update is ready;
// the caller is responsible for draining in-flight requests and calling os.Exit(1).
func New(repo, currentVer string, interval time.Duration, token, channel string, restartSignal chan<- struct{}) *Updater {
	return &Updater{
		repo:          repo,
		currentVer:    currentVer,
		checkInterval: interval,
		token:         token,
		channel:       channel,
		restartSignal: restartSignal,
	}
}

// Start begins the background update check loop. It blocks until ctx is cancelled.
func (u *Updater) Start(ctx context.Context) {
	ticker := time.NewTicker(u.checkInterval)
	defer ticker.Stop()

	// Check once immediately on startup.
	u.check(ctx)

	for {
		select {
		case <-ticker.C:
			u.check(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (u *Updater) check(ctx context.Context) {
	switch u.channel {
	case "develop":
		u.checkDevelop(ctx)
	default:
		u.checkRelease(ctx)
	}
}

// checkRelease polls GitHub Releases for a new tagged version.
func (u *Updater) checkRelease(ctx context.Context) {
	slog.Info("updater: checking for new release", "repo", u.repo, "current", u.currentVer)

	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{
		APIToken: u.token,
	})
	if err != nil {
		slog.Error("updater: failed to create GitHub source", "err", err)
		return
	}

	up, err := selfupdate.NewUpdater(selfupdate.Config{
		Source: source,
		Validator: &selfupdate.ChecksumValidator{
			UniqueFilename: "checksums.txt",
		},
	})
	if err != nil {
		slog.Error("updater: failed to create updater", "err", err)
		return
	}

	latest, found, err := up.DetectLatest(ctx, selfupdate.ParseSlug(u.repo))
	if err != nil {
		slog.Error("updater: release detection failed", "err", err)
		return
	}
	if !found {
		slog.Debug("updater: no release found")
		return
	}
	if latest.LessOrEqual(u.currentVer) {
		slog.Debug("updater: already up to date", "latest", latest.Version())
		return
	}

	slog.Info("updater: new version available", "latest", latest.Version(), "current", u.currentVer)

	exePath, err := selfupdate.ExecutablePath()
	if err != nil {
		slog.Error("updater: could not determine executable path", "err", err)
		return
	}

	if err := up.UpdateTo(ctx, latest, exePath); err != nil {
		slog.Error("updater: update failed", "err", err)
		return
	}

	slog.Info("updater: update applied, signalling restart", "version", latest.Version())
	_ = os.Remove(exePath + ".old")
	close(u.restartSignal)
}

// checkDevelop downloads the latest CI artifact from the develop branch.
// It compares the artifact's created_at timestamp against the running binary's
// modification time to decide whether an update is needed.
func (u *Updater) checkDevelop(ctx context.Context) {
	slog.Info("updater: checking develop CI artifacts", "repo", u.repo)

	exePath, err := os.Executable()
	if err != nil {
		slog.Error("updater: could not determine executable path", "err", err)
		return
	}
	exePath, _ = filepath.EvalSymlinks(exePath)

	// Determine artifact name based on architecture.
	arch := runtime.GOARCH
	if arch == "386" {
		arch = "386"
	}
	artifactName := fmt.Sprintf("windows-%s", arch)

	// Check for tsnet build — if current binary name contains "tsnet", use tsnet artifact.
	baseName := filepath.Base(exePath)
	if baseName == "mdbapi_tsnet.exe" {
		artifactName += "-tsnet"
	}

	// Query GitHub API for latest artifact.
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/actions/artifacts?name=%s&per_page=1",
		u.repo, artifactName)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		slog.Error("updater: build request failed", "err", err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("updater: API request failed", "err", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		slog.Error("updater: API returned non-200", "status", resp.StatusCode)
		return
	}

	var result struct {
		Artifacts []struct {
			Name             string    `json:"name"`
			CreatedAt        time.Time `json:"created_at"`
			ArchiveDownloadURL string  `json:"archive_download_url"`
		} `json:"artifacts"`
		TotalCount int `json:"total_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("updater: decode response failed", "err", err)
		return
	}
	if result.TotalCount == 0 || len(result.Artifacts) == 0 {
		slog.Debug("updater: no develop artifacts found", "name", artifactName)
		return
	}

	artifact := result.Artifacts[0]

	// Compare artifact creation time with current binary modification time.
	exeInfo, err := os.Stat(exePath)
	if err != nil {
		slog.Error("updater: stat executable failed", "err", err)
		return
	}

	if !artifact.CreatedAt.After(exeInfo.ModTime()) {
		slog.Debug("updater: develop build is not newer",
			"artifact", artifact.CreatedAt.Format(time.RFC3339),
			"binary", exeInfo.ModTime().Format(time.RFC3339))
		return
	}

	slog.Info("updater: newer develop build available",
		"artifact", artifact.CreatedAt.Format(time.RFC3339),
		"binary", exeInfo.ModTime().Format(time.RFC3339))

	// Download via nightly.link (no auth required for public repos).
	nightlyURL := fmt.Sprintf("https://nightly.link/%s/workflows/ci.yml/develop/%s.zip",
		u.repo, artifactName)

	if err := u.downloadAndApply(ctx, nightlyURL, exePath); err != nil {
		// Fallback to GitHub API (needs token).
		if u.token == "" {
			slog.Error("updater: nightly.link failed and no token for GitHub API fallback", "err", err)
			return
		}
		slog.Warn("updater: nightly.link failed, trying GitHub API", "err", err)
		if err := u.downloadAndApply(ctx, artifact.ArchiveDownloadURL, exePath); err != nil {
			slog.Error("updater: develop update failed", "err", err)
			return
		}
	}

	slog.Info("updater: develop update applied, signalling restart")
	close(u.restartSignal)
}

// downloadAndApply fetches a zip from url, extracts mdbapi.exe (or mdbapi_tsnet.exe),
// and replaces the running binary.
func (u *Updater) downloadAndApply(ctx context.Context, url, exePath string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	// Write zip to temp file.
	tmpDir, err := os.MkdirTemp("", "mdbapi-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	zipPath := filepath.Join(tmpDir, "artifact.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()

	// Extract the target exe from the zip.
	targetName := filepath.Base(exePath) // "mdbapi.exe" or "mdbapi_tsnet.exe"
	extracted, err := extractFromZip(zipPath, targetName, tmpDir)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Swap: rename current → .old, copy new → current.
	oldPath := exePath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("rename current binary: %w", err)
	}
	if err := copyFile(extracted, exePath); err != nil {
		// Try to restore on failure.
		_ = os.Rename(oldPath, exePath)
		return fmt.Errorf("install new binary: %w", err)
	}
	_ = os.Remove(oldPath)

	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
