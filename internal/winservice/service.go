//go:build windows

// Package winservice integrates with the Windows Service Control Manager.
package winservice

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/kardianos/service"
	"github.com/marcfargas/mdbapi/internal/api"
	"github.com/marcfargas/mdbapi/internal/config"
	"github.com/marcfargas/mdbapi/internal/mdb"
	"github.com/marcfargas/mdbapi/internal/tunnel"
	"github.com/marcfargas/mdbapi/internal/updater"
	"github.com/marcfargas/mdbapi/internal/watcher"
	"golang.org/x/sys/windows/svc/mgr"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Program implements kardianos/service.Interface.
type Program struct {
	cfg     *config.Config
	version string
	commit  string

	// set during Start
	pool          *mdb.Pool
	server        *api.Server
	httpSrv       *http.Server
	tunProv       tunnel.Provider
	cancelCtx     context.CancelFunc
	restartSignal chan struct{}
	watcher       *watcher.Watcher
}

// NewProgram creates a service Program from config.
func NewProgram(cfg *config.Config, version, commit string) *Program {
	return &Program{cfg: cfg, version: version, commit: commit}
}

// Start is called by the SCM or on console run. Must not block.
func (p *Program) Start(s service.Service) error {
	go func() {
		if err := p.run(); err != nil {
			slog.Error("service run error", "err", err)
			s.Stop()
		}
	}()
	return nil
}

// Stop is called by the SCM on stop/shutdown.
func (p *Program) Stop(_ service.Service) error {
	slog.Info("service stopping")
	if p.cancelCtx != nil {
		p.cancelCtx()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if p.httpSrv != nil {
		if err := p.httpSrv.Shutdown(ctx); err != nil {
			slog.Warn("HTTP server shutdown error", "err", err)
		}
	}
	if p.pool != nil {
		p.pool.Close()
	}
	if p.tunProv != nil {
		_ = p.tunProv.Close()
	}
	slog.Info("service stopped")
	return nil
}

func (p *Program) run() error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelCtx = cancel

	// 0. Clean up any stale .old binary from a previous auto-update.
	cleanupOldBinary()

	// 1. Open database connections.
	dbCfgs := make([]mdb.DBConfig, len(p.cfg.Databases))
	for i, d := range p.cfg.Databases {
		dbCfgs[i] = mdb.DBConfig{Alias: d.Alias, Path: d.Path, Driver: d.Driver}
	}
	pool, err := mdb.NewPool(dbCfgs)
	if err != nil {
		return fmt.Errorf("open databases: %w", err)
	}
	p.pool = pool

	// 2. Build HTTP server via Store interface.
	store := mdb.NewPoolStore(pool)
	p.server = api.NewServer(store, p.cfg.API.MaxRows, p.version, p.commit)
	handler := p.server.Handler(api.HandlerConfig{
		Keys:           p.cfg.Auth.Keys,
		MaxBodyBytes:   p.cfg.Server.MaxBodySize,
		AllowedIPs:     p.cfg.Auth.AllowedIPs,
		RateLimit:      p.cfg.Server.RateLimit,
		RateBurst:      p.cfg.Server.RateBurst,
		RequestTimeout: p.cfg.Server.RequestTimeout,
	})

	// 3. Create tunnel provider and bind listener.
	tunProv, err := tunnel.New(p.cfg.Tunnel)
	if err != nil {
		return fmt.Errorf("tunnel init: %w", err)
	}
	p.tunProv = tunProv

	ln, err := tunProv.Listen(p.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", p.cfg.Server.Listen, err)
	}

	p.httpSrv = &http.Server{
		Handler:        handler,
		ReadTimeout:    p.cfg.Server.ReadTimeout,
		WriteTimeout:   p.cfg.Server.WriteTimeout,
		IdleTimeout:    p.cfg.Server.IdleTimeout,
		MaxHeaderBytes: p.cfg.Server.MaxHeaderBytes,
	}

	slog.Info("service started",
		"listen", listenAddr(ln),
		"databases", len(p.cfg.Databases),
		"tunnel", p.cfg.Tunnel.Provider,
		"version", p.version,
	)

	// 4. Start auto-updater if enabled.
	p.restartSignal = make(chan struct{})
	if p.cfg.Updater.Enabled {
		u := updater.New(
			p.cfg.Updater.GithubRepo,
			p.version,
			p.commit,
			p.cfg.Updater.CheckInterval,
			p.cfg.Updater.GithubToken,
			p.cfg.Updater.Channel,
			p.cfg.Updater.Variant,
			p.restartSignal,
		)
		go u.Start(ctx)

		go func() {
			select {
			case <-p.restartSignal:
				slog.Info("auto-update complete, restarting via SCM")
				_ = p.Stop(nil)
				os.Exit(1) // SCM recovery action restarts with new binary after 5s
			case <-ctx.Done():
			}
		}()
	}

	// 4b. Start glob watcher for dynamic database discovery.
	if len(p.cfg.GlobEntries) > 0 {
		w, err := watcher.New(p.cfg.GlobEntries, pool, 30*time.Second)
		if err != nil {
			slog.Warn("glob watcher init failed", "err", err)
		} else {
			p.watcher = w
			go w.Run(ctx)
			slog.Info("glob watcher started", "patterns", len(p.cfg.GlobEntries))
		}
	}

	// 5. Serve — blocks until Shutdown is called.
	if p.cfg.Server.TLSCert != "" && p.cfg.Server.TLSKey != "" {
		slog.Info("TLS enabled", "cert", p.cfg.Server.TLSCert)
		return filterServeError(p.httpSrv.ServeTLS(ln, p.cfg.Server.TLSCert, p.cfg.Server.TLSKey))
	}
	return filterServeError(p.httpSrv.Serve(ln))
}

// filterServeError suppresses http.ErrServerClosed which is the normal shutdown path.
func filterServeError(err error) error {
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func listenAddr(ln net.Listener) string {
	if ln == nil {
		return "unknown"
	}
	return ln.Addr().String()
}

// cleanupOldBinary removes the stale .old backup left by a previous auto-update.
// The file may still be locked if the previous restart was very recent; errors are ignored.
func cleanupOldBinary() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	old := exe + ".old"
	if _, err := os.Stat(old); err == nil {
		if err := os.Remove(old); err != nil {
			slog.Debug("could not remove stale backup binary (may still be locked)", "path", old, "err", err)
		} else {
			slog.Info("removed stale backup binary", "path", old)
		}
	}
}

// SetupLogging configures slog to write to the configured log file with rotation.
// Call this before starting the service loop.
func SetupLogging(logFile string) error {
	if logFile == "" {
		return nil // keep default stderr
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0700); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	w := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    50, // MB
		MaxBackups: 5,
		MaxAge:     30, // days
		Compress:   true,
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	return nil
}

// Install creates the Windows service and configures SCM recovery actions.
// The service runs as NT SERVICE\<name> — a virtual account with minimal permissions.
func Install(svcCfg *service.Config, prg service.Interface, svcName string) error {
	svc, err := service.New(prg, svcCfg)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	if err := svc.Install(); err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	return configureRecovery(svcName)
}

// configureRecovery sets SCM recovery actions so the service restarts on any failure,
// including a clean os.Exit(1) from the auto-updater.
func configureRecovery(svcName string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("open service %q in SCM: %w", svcName, err)
	}
	defer s.Close()

	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(actions, uint32((time.Hour).Seconds())); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}
	// CRITICAL: without this, os.Exit(1) does NOT trigger recovery — only crashes do.
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("enable recovery on non-crash exit: %w", err)
	}
	return nil
}
