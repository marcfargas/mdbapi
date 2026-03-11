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
//   - Users: (no access)
//
// This prevents local users from reading the config file and API keys.
// It uses icacls.exe which is available on all supported Windows versions.
func FixACL(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create directory %q: %w", dir, err)
		}
	}

	// Remove all inherited and explicit ACEs, then set explicit grants.
	cmds := [][]string{
		// Disable inheritance and remove inherited ACEs.
		{"icacls", dir, "/inheritance:d"},
		// Remove all existing explicit ACEs.
		{"icacls", dir, "/remove:g", "Everyone"},
		{"icacls", dir, "/remove:g", "Users"},
		{"icacls", dir, "/remove:g", "Authenticated Users"},
		// Grant SYSTEM and Administrators.
		{"icacls", dir, "/grant", "SYSTEM:(OI)(CI)F"},
		{"icacls", dir, "/grant", "Administrators:(OI)(CI)F"},
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("icacls %v: %w\n%s", args[1:], err, string(out))
		}
	}
	return nil
}
