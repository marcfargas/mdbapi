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
			// Match only MDB/ACCDB drivers, not dBASE or other "Access"-branded drivers
			// (e.g. "Microsoft Access dBASE Driver (*.dbf, *.ndx, *.mdx)" must be excluded).
			if strings.Contains(n, ".mdb") {
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

// openDB opens an Access database with the selected ODBC driver.
// If explicitDriver is empty, it uses the first installed Access driver.
func openDB(path, explicitDriver string) (*sql.DB, string, error) {
	driverName := explicitDriver
	if driverName == "" {
		drivers := installedAccessDrivers()
		if len(drivers) == 0 {
			return nil, "", fmt.Errorf(
				"no Microsoft Access ODBC driver found; " +
					"install Access Database Engine 2010 or 2016")
		}
		driverName = drivers[0]
	}
	return openDBWithDriver(path, driverName)
}

// openDBWithDriver opens path using a specific named ODBC driver.
//
// ReadOnly=1 is intentionally omitted: ACE 2016 blocks MSysObjects access
// (needed for table listing) when ReadOnly=1 is set, regardless of Uid/Pwd.
// Write protection is enforced at the SQL layer (SELECT-only validation in
// query.go) and at the OS layer (NTFS read-only permissions on the data dir
// via mdbapi fixacl). The .ldb lock file created by ACE is harmless and is
// cleaned up when the connection closes.
//
// Uid=Admin;Pwd= grants access to MSysObjects on databases without workgroup
// security (the default for the vast majority of Access files in the wild).
func openDBWithDriver(path, driverName string) (*sql.DB, string, error) {
	connStr := fmt.Sprintf(
		`DRIVER={%s};DBQ=%s;Uid=Admin;Pwd=;`,
		driverName, path,
	)
	db, err := sql.Open("odbc", connStr)
	if err != nil {
		return nil, "", err
	}
	db.SetMaxOpenConns(MaxOpenConns)
	db.SetMaxIdleConns(MaxIdleConns)
	db.SetConnMaxLifetime(ConnMaxLifetime)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, "", err
	}
	return db, connStr, nil
}
