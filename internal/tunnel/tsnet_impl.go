//go:build tsnet

package tunnel

import (
	"fmt"
	"net"

	"github.com/marcfargas/mdbapi/internal/config"
	"tailscale.com/tsnet"
)

type tsnetProvider struct {
	srv    *tsnet.Server
	funnel bool
}

func newTsnet(cfg config.TsnetConfig) (Provider, error) {
	if cfg.Hostname == "" {
		return nil, fmt.Errorf("tsnet: hostname is required")
	}
	if cfg.AuthKey == "" {
		return nil, fmt.Errorf("tsnet: auth_key is required (use env var TS_AUTHKEY)")
	}
	srv := &tsnet.Server{
		Hostname: cfg.Hostname,
		AuthKey:  cfg.AuthKey,
		Dir:      cfg.StateDir,
	}
	return &tsnetProvider{srv: srv, funnel: cfg.Funnel}, nil
}

func (t *tsnetProvider) Listen(addr string) (net.Listener, error) {
	if t.funnel {
		// Funnel exposes the service publicly via Tailscale's internet proxy.
		// Only ports 443, 8443, and 10000 are supported.
		ln, err := t.srv.ListenFunnel("tcp", ":443")
		if err != nil {
			return nil, fmt.Errorf("tsnet funnel listen: %w", err)
		}
		return ln, nil
	}
	// Tailnet-only (no public exposure).
	ln, err := t.srv.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tsnet listen: %w", err)
	}
	return ln, nil
}

func (t *tsnetProvider) Close() error {
	return t.srv.Close()
}
