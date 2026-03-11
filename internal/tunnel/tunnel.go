// Package tunnel provides a pluggable tunneling abstraction.
// The default provider is "none" (local listener only).
// Build with -tags tsnet to include Tailscale tsnet support.
package tunnel

import (
	"fmt"
	"net"

	"github.com/marcfargas/mdbapi/internal/config"
)

// Provider abstracts the network listener used by the HTTP server.
// For "none" this is a plain TCP listener. For tsnet it is a Tailscale node.
type Provider interface {
	// Listen returns a net.Listener on the given address.
	Listen(addr string) (net.Listener, error)
	// Close releases all resources held by the provider.
	Close() error
}

// New creates a Provider from the tunnel config.
// Returns ErrProviderNotBuiltIn if the requested provider requires a build tag
// that was not included at compile time.
func New(cfg config.TunnelConfig) (Provider, error) {
	switch cfg.Provider {
	case "none", "":
		return &noneProvider{}, nil
	case "tsnet":
		return newTsnet(cfg.Tsnet)
	default:
		return nil, fmt.Errorf("unknown tunnel provider %q", cfg.Provider)
	}
}

// ErrProviderNotBuiltIn is returned when a provider requires a build tag that
// was not included at compile time (e.g., "tsnet" without -tags tsnet).
type ErrProviderNotBuiltIn struct {
	Provider string
	BuildTag string
}

func (e ErrProviderNotBuiltIn) Error() string {
	return fmt.Sprintf(
		"tunnel provider %q requires build tag %q: rebuild with 'go build -tags %s'",
		e.Provider, e.BuildTag, e.BuildTag,
	)
}
