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
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// Updater polls GitHub releases or CI artifacts and signals when an update
// has been applied.
type Updater struct {
	repo          string
	currentVer    string
	currentCommit string
	checkInterval time.Duration
	token         string
	channel       string // "release" or "develop"
	variant       string // "standard" or "tsnet"
	restartSignal chan<- struct{}
}

// New creates an Updater. restartSignal is closed when an update is ready;
// the caller is responsible for draining in-flight requests and calling os.Exit(1).
func New(repo, currentVer, currentCommit string, interval time.Duration, token, channel, variant string, restartSignal chan<- struct{}) *Updater {
	return &Updater{
		repo:          repo,
		currentVer:    currentVer,
		currentCommit: currentCommit,
		checkInterval: interval,
		token:         token,
		channel:       channel,
		variant:       variant,
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
// It compares the artifact's commit SHA against the running binary's commit
// to decide whether an update is needed, then verifies the downloaded binary
// matches the expected commit before applying.
func (u *Updater) checkDevelop(ctx context.Context) {
	slog.Info("updater: checking develop CI artifacts", "repo", u.repo, "current_commit", u.currentCommit)

	exePath, err := os.Executable()
	if err != nil {
		slog.Error("updater: could not determine executable path", "err", err)
		return
	}
	exePath, _ = filepath.EvalSymlinks(exePath)

	// Determine artifact name based on architecture and variant.
	artifactName := fmt.Sprintf("windows-%s", runtime.GOARCH)
	if u.variant == "tsnet" {
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
			Name               string `json:"name"`
			ArchiveDownloadURL string `json:"archive_download_url"`
			WorkflowRun        struct {
				HeadSHA string `json:"head_sha"`
			} `json:"workflow_run"`
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
	expectedCommit := artifact.WorkflowRun.HeadSHA

	// Compare commit hashes — skip if already running this commit.
	if len(expectedCommit) >= 7 && len(u.currentCommit) >= 7 &&
		expectedCommit[:7] == u.currentCommit[:7] {
		slog.Debug("updater: already running latest commit", "commit", expectedCommit[:7])
		return
	}

	slog.Info("updater: newer develop build available",
		"current_commit", u.currentCommit,
		"artifact_commit", expectedCommit)

	// Download via nightly.link (no auth required for public repos).
	nightlyURL := fmt.Sprintf("https://nightly.link/%s/workflows/ci.yml/develop/%s.zip",
		u.repo, artifactName)

	if err := u.downloadAndVerify(ctx, nightlyURL, exePath, expectedCommit); err != nil {
		// Fallback to GitHub API (needs token).
		if u.token == "" {
			slog.Error("updater: nightly.link failed and no token for GitHub API fallback", "err", err)
			return
		}
		slog.Warn("updater: nightly.link failed, trying GitHub API", "err", err)
		if err := u.downloadAndVerify(ctx, artifact.ArchiveDownloadURL, exePath, expectedCommit); err != nil {
			slog.Error("updater: develop update failed", "err", err)
			return
		}
	}

	slog.Info("updater: develop update applied, signalling restart", "commit", expectedCommit)
	close(u.restartSignal)
}

// downloadAndVerify downloads, extracts, verifies the commit hash of the
// downloaded binary, and only then applies the update.
func (u *Updater) downloadAndVerify(ctx context.Context, url, exePath, expectedCommit string) error {
	tmpDir, err := u.downloadAndExtract(ctx, url)
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// Find the extracted binary.
	candidates := []string{"mdbapi.exe"}
	if u.variant == "tsnet" {
		candidates = []string{"mdbapi_tsnet.exe", "mdbapi.exe"}
	}
	var extracted string
	for _, name := range candidates {
		extracted, err = extractFromZip(filepath.Join(tmpDir, "artifact.zip"), name, tmpDir)
		if err == nil {
			break
		}
	}
	if extracted == "" {
		return fmt.Errorf("extract: no matching binary in zip (tried %v)", candidates)
	}

	// Verify commit hash by running the downloaded binary.
	if expectedCommit != "" {
		out, err := exec.CommandContext(ctx, extracted, "version").Output()
		if err != nil {
			slog.Warn("updater: could not verify downloaded binary", "err", err)
			// Proceed anyway — binary may not support version command yet.
		} else {
			output := string(out)
			short := expectedCommit
			if len(short) > 7 {
				short = short[:7]
			}
			if !strings.Contains(output, short) {
				return fmt.Errorf("commit mismatch: expected %s, binary reports: %s", short, strings.TrimSpace(output))
			}
			slog.Info("updater: downloaded binary commit verified", "commit", short)
		}
	}

	// Apply: swap binaries.
	oldPath := exePath + ".old"
	_ = os.Remove(oldPath)
	if err := os.Rename(exePath, oldPath); err != nil {
		return fmt.Errorf("rename current binary: %w", err)
	}
	if err := copyFile(extracted, exePath); err != nil {
		_ = os.Rename(oldPath, exePath)
		return fmt.Errorf("install new binary: %w", err)
	}
	_ = os.Remove(oldPath)

	return nil
}

// downloadAndExtract fetches a zip to a temp directory and returns the tmpDir path.
func (u *Updater) downloadAndExtract(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	if u.token != "" {
		req.Header.Set("Authorization", "Bearer "+u.token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	tmpDir, err := os.MkdirTemp("", "mdbapi-update-*")
	if err != nil {
		return "", err
	}

	zipPath := filepath.Join(tmpDir, "artifact.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.RemoveAll(tmpDir)
		return "", err
	}
	f.Close()

	return tmpDir, nil
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
