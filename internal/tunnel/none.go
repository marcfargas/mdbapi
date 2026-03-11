package tunnel

import "net"

// noneProvider is a pass-through that binds a plain TCP listener.
// This is the default and adds zero binary size overhead.
type noneProvider struct{}

func (n *noneProvider) Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

func (n *noneProvider) Close() error { return nil }
