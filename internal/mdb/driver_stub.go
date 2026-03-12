//go:build !windows

package mdb

import (
	"database/sql"
	"fmt"
)

var errODBCNotAvailable = fmt.Errorf("ODBC Access driver only available on Windows")

func openDB(path, explicitDriver string) (*sql.DB, error) {
	// ODBC is only available on Windows.
	// This stub satisfies the compiler on non-Windows platforms.
	return nil, errODBCNotAvailable
}
