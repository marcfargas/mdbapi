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
	"time"

	"github.com/kardianos/service"
	"github.com/marcfargas/mdbapi/internal/api"
	"github.com/marcfargas/mdbapi/internal/config"
	"github.com/marcfargas/mdbapi/internal/mdb"
	"github.com/marcfargas/mdbapi/internal/tunnel"
	"github.com/marcfargas/mdbapi/internal/updater"
	"golang.org/x/sys/windows/svc/mgr"
)

// Program implements kardianos/service.Interface.
type Program struct {
	cfg     *config.Config
	version string

	// set during Start
	pool      *mdb.Pool
	server    *api.Server
	httpSrv   *http.Server
	tunProv   tunnel.Provider
	cancelCtx context.CancelFunc

	restartSignal chan struct{}
}

// NewProgram creates a service Program from config.
func NewProgram(cfg *config.Config, version string) *Program {
	return &Program{cfg: cfg, version: version}
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

// Stop is called by the SCM on stop/shutdown. Must not block longer than timeout.
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
	return nil
}

func (p *Program) run() error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelCtx = cancel

	// 1. Open database connections.
	dbCfgs := make([]mdb.DBConfig, len(p.cfg.Databases))
	for i, d := range p.cfg.Databases {
		dbCfgs[i] = mdb.DBConfig{Alias: d.Alias, Path: d.Path}
	}
	pool, err := mdb.NewPool(dbCfgs)
	if err != nil {
		return fmt.Errorf("open databases: %w", err)
	}
	p.pool = pool

	// 2. Create HTTP server.
	p.server = api.NewServer(pool, p.cfg.API.MaxRows, p.version)
	handler := p.server.Handler(p.cfg.Auth.Keys, p.cfg.Server.MaxBodySize)

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
		Handler:      handler,
		ReadTimeout:  p.cfg.Server.ReadTimeout,
		WriteTimeout: p.cfg.Server.WriteTimeout,
	}

	// 4. TLS if configured.
	slog.Info("service started",
		"listen", listenAddr(ln),
		"databases", len(p.cfg.Databases),
		"tunnel", p.cfg.Tunnel.Provider,
		"version", p.version,
	)

	// 5. Start updater if enabled.
	p.restartSignal = make(chan struct{})
	if p.cfg.Updater.Enabled {
		u := updater.New(
			p.cfg.Updater.GithubRepo,
			p.version,
			p.cfg.Updater.CheckInterval,
			p.cfg.Updater.GithubToken,
			p.restartSignal,
		)
		go u.Start(ctx)

		// Watch for restart signal — graceful shutdown before SCM restarts us.
		go func() {
			select {
			case <-p.restartSignal:
				slog.Info("updater restart signal received, stopping service")
				_ = p.Stop(nil)
				os.Exit(1) // SCM recovery action restarts with new binary
			case <-ctx.Done():
			}
		}()
	}

	// 6. Serve — blocks until Shutdown is called.
	if p.cfg.Server.TLSCert != "" && p.cfg.Server.TLSKey != "" {
		return p.httpSrv.ServeTLS(ln, p.cfg.Server.TLSCert, p.cfg.Server.TLSKey)
	}
	return p.httpSrv.Serve(ln)
}

func listenAddr(ln net.Listener) string {
	if ln == nil {
		return "unknown"
	}
	return ln.Addr().String()
}

// Install creates the Windows service entry and configures SCM recovery actions.
// kardianos/service creates the service; we then add recovery actions via x/sys/windows.
func Install(svc service.Service, svcName string) error {
	if err := svc.Install(); err != nil {
		return fmt.Errorf("install service: %w", err)
	}
	return configureRecovery(svcName)
}

// configureRecovery sets SCM recovery actions so the service restarts on failure
// (including clean exit(1) from the updater).
func configureRecovery(svcName string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(svcName)
	if err != nil {
		return fmt.Errorf("open service %q: %w", svcName, err)
	}
	defer s.Close()

	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	resetPeriod := uint32((1 * time.Hour).Seconds())
	if err := s.SetRecoveryActions(actions, resetPeriod); err != nil {
		return fmt.Errorf("set recovery actions: %w", err)
	}

	// CRITICAL: enable recovery on non-crash exits too (exit code 1 from updater).
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("set recovery on non-crash failures: %w", err)
	}
	return nil
}
