//go:build !tsnet

package tunnel

import "github.com/marcfargas/mdbapi/internal/config"

// newTsnet returns an error when the binary was not built with -tags tsnet.
func newTsnet(_ config.TsnetConfig) (Provider, error) {
	return nil, ErrProviderNotBuiltIn{Provider: "tsnet", BuildTag: "tsnet"}
}
