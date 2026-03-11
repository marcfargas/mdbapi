// driver_windows_amd64.go registers the ODBC driver with database/sql.
// alexbrainman/odbc supports windows/386 and windows/amd64 only.
// ARM64 Windows uses x64 emulation to run the amd64 binary, so this is fine.
package mdb

import _ "github.com/alexbrainman/odbc"
