//go:build windows && (amd64 || 386)

// driver_odbc.go registers the ODBC driver with database/sql.
// alexbrainman/odbc supports windows/386 and windows/amd64 only.
package mdb

import _ "github.com/alexbrainman/odbc"
