//go:build !windows

package mdb

import "context"

// listTablesViaODBCSyscall is only available on Windows.
func listTablesViaODBCSyscall(_ context.Context, _ string) ([]string, error) {
	return nil, errODBCNotAvailable
}
