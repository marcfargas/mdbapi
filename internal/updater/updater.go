// Package updater checks for new releases on GitHub and applies them.
// The update flow is two-phase: download+verify, then signal for restart.
// The actual restart is handled by Windows SCM recovery actions (exit code 1).
package updater

import (
	"context"
	"log/slog"
	"os"
	"time"

	selfupdate "github.com/creativeprojects/go-selfupdate"
)

// Updater polls GitHub releases and signals when an update has been applied.
type Updater struct {
	repo          string
	currentVer    string
	checkInterval time.Duration
	token         string
	restartSignal chan<- struct{}
}

// New creates an Updater. restartSignal is closed when an update is ready;
// the caller is responsible for draining in-flight requests and calling os.Exit(1).
func New(repo, currentVer string, interval time.Duration, token string, restartSignal chan<- struct{}) *Updater {
	return &Updater{
		repo:          repo,
		currentVer:    currentVer,
		checkInterval: interval,
		token:         token,
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
	slog.Info("updater: checking for new release", "repo", u.repo, "current", u.currentVer)

	source, err := selfupdate.NewGitHubSource(selfupdate.GitHubConfig{
		APIToken: u.token,
	})
	if err != nil {
		slog.Error("updater: failed to create GitHub source", "err", err)
		return
	}

	updater, err := selfupdate.NewUpdater(selfupdate.Config{
		Source: source,
		Validator: &selfupdate.ChecksumValidator{
			UniqueFilename: "checksums.txt",
		},
	})
	if err != nil {
		slog.Error("updater: failed to create updater", "err", err)
		return
	}

	latest, found, err := updater.DetectLatest(ctx, selfupdate.ParseSlug(u.repo))
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

	if err := updater.UpdateTo(ctx, latest, exePath); err != nil {
		slog.Error("updater: update failed", "err", err)
		return
	}

	slog.Info("updater: update applied, signalling restart", "version", latest.Version())

	// Clean up stale .old backup from previous update (best-effort;
	// may still be locked if the previous restart is still in progress).
	_ = os.Remove(exePath + ".old")

	// Signal the service to drain in-flight requests and restart.
	close(u.restartSignal)
}
