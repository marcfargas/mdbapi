package mdb

import (
	"database/sql"
	"fmt"

	_ "github.com/alexbrainman/odbc"
)

// ProbeDriver attempts to open an ODBC connection to verify the Access driver
// is installed and matches the binary architecture (32 vs 64 bit).
// It expects a "file not found" type error, not a "driver not found" error.
func ProbeDriver() (string, error) {
	// Connect with a non-existent file — we only care whether the driver loads.
	const connStr = `DRIVER={Microsoft Access Driver (*.mdb, *.accdb)};DBQ=__probe__.mdb;`
	db, err := sql.Open("odbc", connStr)
	if err != nil {
		return "", fmt.Errorf("ODBC driver not available: %w", err)
	}
	defer db.Close()

	pingErr := db.Ping()
	if pingErr == nil {
		return "driver loaded", nil
	}
	// "Could not find file" means the driver loaded successfully.
	errStr := pingErr.Error()
	if contains(errStr, "Could not find file") ||
		contains(errStr, "not a valid path") ||
		contains(errStr, "file not found") {
		return "driver loaded (file probe succeeded)", nil
	}
	// Any other error may indicate a driver architecture mismatch.
	return "", fmt.Errorf("driver probe error (check 32/64-bit match): %w", pingErr)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
