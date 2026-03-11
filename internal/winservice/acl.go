//go:build windows

package winservice

import (
	"fmt"
	"os"
	"os/exec"
)

// FixACL sets restrictive Windows ACLs on dir:
//   - SYSTEM: Full Control
//   - Administrators: Full Control
//   - NT SERVICE\<svcName>: Read & Execute (so the service can read its config)
//   - Everyone / Users / Authenticated Users: removed
//
// svcName is the Windows service name (e.g. "MDBRestService").
// NT SERVICE virtual accounts are created automatically by the SCM when the
// service first starts — no manual account creation is required.
//
// FixACL uses icacls.exe, available on all supported Windows versions.
func FixACL(dir, svcName string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}

	virtualAccount := `NT SERVICE\` + svcName

	cmds := [][]string{
		// Disable inheritance and remove inherited ACEs.
		{"icacls", dir, "/inheritance:d"},
		// Remove broad grants.
		{"icacls", dir, "/remove:g", "Everyone"},
		{"icacls", dir, "/remove:g", "Users"},
		{"icacls", dir, "/remove:g", "Authenticated Users"},
		// SYSTEM and Administrators: full control.
		{"icacls", dir, "/grant", "SYSTEM:(OI)(CI)F"},
		{"icacls", dir, "/grant", "Administrators:(OI)(CI)F"},
		// Service virtual account: read & execute (config + log, no write).
		{"icacls", dir, "/grant", virtualAccount + ":(OI)(CI)RX"},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("icacls %v: %w\n%s", args[1:], err, string(out))
		}
	}
	return nil
}
