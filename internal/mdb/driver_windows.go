//go:build windows

package mdb

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sys/windows/registry"
)

// preferredAccessDrivers is the priority order for Access ODBC drivers.
// Newer drivers support .accdb but may reject Access 97 .mdb files.
// Older drivers (Jet 4.0 / ACE 2007-2013) support Access 97.
var preferredAccessDrivers = []string{
	"Microsoft Access Driver (*.mdb, *.accdb)", // ACE 2007+
	"Microsoft Access Driver (*.mdb)",          // Jet 4.0 / older ACE
}

var (
	driverCacheOnce sync.Once
	driverCache     []string
)

// installedAccessDrivers enumerates ODBC drivers from the Windows registry
// and returns all Microsoft Access drivers, in preferredAccessDrivers order
// first, then any others.
func installedAccessDrivers() []string {
	driverCacheOnce.Do(func() {
		k, err := registry.OpenKey(
			registry.LOCAL_MACHINE,
			`SOFTWARE\ODBC\ODBCINST.INI\ODBC Drivers`,
			registry.READ,
		)
		if err != nil {
			driverCache = append([]string{}, preferredAccessDrivers...)
			return
		}
		defer k.Close()

		names, err := k.ReadValueNames(0)
		if err != nil {
			driverCache = append([]string{}, preferredAccessDrivers...)
			return
		}

		installed := make(map[string]bool, len(names))
		for _, n := range names {
			if strings.Contains(strings.ToLower(n), "access") {
				installed[n] = true
			}
		}

		// Preferred order first.
		for _, d := range preferredAccessDrivers {
			if installed[d] {
				driverCache = append(driverCache, d)
				delete(installed, d)
			}
		}
		// Any remaining Access drivers.
		for d := range installed {
			driverCache = append(driverCache, d)
		}

		if len(driverCache) == 0 {
			// No Access drivers found — fall back to trying the known names;
			// the error from sql.Open will be informative.
			driverCache = append([]string{}, preferredAccessDrivers...)
		}
	})
	return driverCache
}

// isFallbackError returns true when the ODBC error is a format compatibility
// or driver-not-installed error that warrants trying the next driver.
func isFallbackError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "previous version") ||
		strings.Contains(msg, "im002") || // Data source name not found
		strings.Contains(msg, "im003") || // Driver load error
		strings.Contains(msg, "could not be loaded")
}

// openDB opens an Access database with automatic driver fallback.
// If explicitDriver is non-empty, only that driver is tried (no fallback).
func openDB(path, explicitDriver string) (*sql.DB, error) {
	drivers := installedAccessDrivers()
	if explicitDriver != "" {
		drivers = []string{explicitDriver}
	}

	var lastErr error
	for _, driverName := range drivers {
		db, err := openDBWithDriver(path, driverName)
		if err == nil {
			return db, nil
		}
		lastErr = err
		if !isFallbackError(err) {
			// Real error (file not found, permissions, etc.) — stop immediately.
			return nil, fmt.Errorf("driver %q: %w", driverName, err)
		}
		// Format mismatch or driver not installed — try next.
	}

	tried := strings.Join(drivers, ", ")
	return nil, fmt.Errorf("no compatible Access driver found (tried: %s): %w", tried, lastErr)
}

// openDBWithDriver opens path using a specific named ODBC driver.
func openDBWithDriver(path, driverName string) (*sql.DB, error) {
	connStr := fmt.Sprintf(
		`DRIVER={%s};DBQ=%s;ReadOnly=1;`,
		driverName, path,
	)
	db, err := sql.Open("odbc", connStr)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	db.SetConnMaxLifetime(ConnMaxLifetime)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
