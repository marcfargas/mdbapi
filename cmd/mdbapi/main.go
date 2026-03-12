//go:build windows

// mdbapi is a self-contained Windows service that exposes Microsoft Access
// databases (MDB/ACCDB) via a read-only REST API.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/kardianos/service"
	"github.com/marcfargas/mdbapi/internal/config"
	"github.com/marcfargas/mdbapi/internal/mdb"
	"github.com/marcfargas/mdbapi/internal/winservice"
)

// Build-time variables injected via -ldflags:
//   -X main.Version=1.0.0 -X main.Commit=abc1234 -X main.Variant=tsnet
var (
	Version = "dev"
	Commit  = "unknown"
	Variant = "standard"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "run":
		runCmd(args)
	case "install":
		installCmd(args)
	case "uninstall":
		lifecycleCmd("uninstall", args)
	case "start":
		lifecycleCmd("start", args)
	case "stop":
		lifecycleCmd("stop", args)
	case "check":
		checkCmd(args)
	case "keygen":
		keygenCmd(args)
	case "fixacl":
		fixaclCmd(args)
	case "version":
		fmt.Printf("mdbapi %s %s/%s commit=%s\n", Version, runtime.GOARCH, Variant, Commit)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %q\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

// runCmd starts the service in console (foreground) mode.
// When started by the SCM, it also uses this path (with -config argument).
func runCmd(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config file")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatalf("config: %v", err)
	}

	// When log_file is set (service mode), redirect slog to rotating file.
	// In foreground dev mode, leave as stderr (log_file defaults to path but
	// the operator can clear it for interactive use).
	if !service.Interactive() {
		if err := winservice.SetupLogging(cfg.Service.LogFile); err != nil {
			fatalf("setup logging: %v", err)
		}
	}

	prg := winservice.NewProgram(cfg, Version)
	svcCfg := serviceConfig(cfg)
	svc, err := service.New(prg, svcCfg)
	if err != nil {
		fatalf("service init: %v", err)
	}
	if err := svc.Run(); err != nil {
		fatalf("service run: %v", err)
	}
}

// installCmd installs the Windows service and configures SCM recovery actions.
func installCmd(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config file")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatalf("config: %v", err)
	}

	prg := winservice.NewProgram(cfg, Version)
	svcCfg := serviceConfig(cfg)
	svcCfg.Arguments = []string{"run", "-config", *cfgPath}

	if err := winservice.Install(svcCfg, prg, cfg.Service.Name); err != nil {
		fatalf("install: %v", err)
	}
	fmt.Printf("Service %q installed.\n", cfg.Service.Name)
	fmt.Printf("Account:  NT SERVICE\\%s\n", cfg.Service.Name)
	fmt.Printf("Log file: %s\n", cfg.Service.LogFile)
	fmt.Println("\nNext steps:")
	fmt.Printf("  mdbapi fixacl        # restrict config dir ACLs\n")
	fmt.Printf("  mdbapi start         # start the service\n")
}

// lifecycleCmd handles uninstall/start/stop.
func lifecycleCmd(action string, args []string) {
	fs := flag.NewFlagSet(action, flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config file")
	_ = fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatalf("config: %v", err)
	}

	prg := winservice.NewProgram(cfg, Version)
	svc, err := service.New(prg, serviceConfig(cfg))
	if err != nil {
		fatalf("service init: %v", err)
	}

	switch action {
	case "uninstall":
		err = svc.Uninstall()
	case "start":
		err = svc.Start()
	case "stop":
		err = svc.Stop()
	}
	if err != nil {
		fatalf("%s: %v", action, err)
	}
	fmt.Printf("Service %q: %s OK\n", cfg.Service.Name, action)
}

// checkCmd validates config and tests all database connections.
func checkCmd(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	cfgPath := fs.String("config", defaultConfigPath(), "path to config file")
	_ = fs.Parse(args)

	fmt.Printf("mdbapi %s — configuration check\n\n", Version)
	hasError := false

	// 1. Load config.
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Printf("✗ Config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Config loaded: %s\n", *cfgPath)

	// 2. Check auth keys.
	fmt.Printf("✓ Auth keys: %d configured\n", len(cfg.Auth.Keys))

	// 3. Check ODBC driver availability.
	if err := checkODBCDriver(); err != nil {
		fmt.Printf("✗ ODBC driver: %v\n", err)
		hasError = true
	} else {
		fmt.Println("✓ ODBC driver: Microsoft Access Driver found")
	}

	// 4. Test each database connection.
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ALIAS\tPATH\tSTATUS\tTABLES")

	for _, db := range cfg.Databases {
		status, tableCount := testDatabase(db.Path, db.Driver)
		mark := "✓"
		if strings.HasPrefix(status, "✗") {
			mark = "✗"
			hasError = true
		}
		_ = mark
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", db.Alias, db.Path, status, tableCount)
	}
	_ = w.Flush()

	if hasError {
		fmt.Println("\n✗ Check failed — see errors above")
		os.Exit(1)
	}
	fmt.Println("\n✓ All checks passed")
}

// keygenCmd generates a random API key and prints it.
// With -add-to-config it appends the key to the config file's auth.keys list.
func keygenCmd(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	addToConfig := fs.String("add-to-config", "", "path to config file to add the key to")
	_ = fs.Parse(args)

	key := generateKey()
	fmt.Printf("Generated key: %s\n", key)

	if *addToConfig != "" {
		if err := appendKeyToConfig(*addToConfig, key); err != nil {
			fatalf("add to config: %v", err)
		}
		fmt.Printf("Key appended to %s\n", *addToConfig)
		fmt.Println("Restart the service for the new key to take effect.")
	} else {
		fmt.Println("\nTo add to config:")
		fmt.Printf("  mdbapi keygen -add-to-config %s\n", defaultConfigPath())
		fmt.Println("Or set as environment variable and reference in config:")
		fmt.Printf("  auth:\n    keys:\n      - \"${MDBAPI_KEY}\"\n")
	}
}

// fixaclCmd sets restrictive ACLs on the service config directory.
func fixaclCmd(args []string) {
	fs := flag.NewFlagSet("fixacl", flag.ExitOnError)
	dir := fs.String("dir", filepath.Dir(defaultConfigPath()), "directory to secure")
	cfg := fs.String("config", defaultConfigPath(), "path to config (for service name)")
	_ = fs.Parse(args)

	// Read service name from config so we can grant the correct virtual account.
	// Fall back to the default name if config can't be loaded.
	svcName := "MDBRestService"
	if c, err := config.Load(*cfg); err == nil {
		svcName = c.Service.Name
	}

	if err := winservice.FixACL(*dir, svcName); err != nil {
		fatalf("fixacl: %v", err)
	}
	fmt.Printf("✓ ACLs set on %s\n", *dir)
	fmt.Println("  SYSTEM:                          Full Control")
	fmt.Println("  Administrators:                  Full Control")
	fmt.Printf("  NT SERVICE\\%s:  Read & Execute\n", svcName)
	fmt.Println("  Everyone / Users:                (none)")
}

// --- helpers ---

func serviceConfig(cfg *config.Config) *service.Config {
	return &service.Config{
		Name:        cfg.Service.Name,
		DisplayName: cfg.Service.DisplayName,
		Description: cfg.Service.Description,
		// Run as a virtual NT SERVICE account — no password, minimal privileges.
		// Windows creates this account automatically on service install.
		UserName: `NT SERVICE\` + cfg.Service.Name,
	}
}

func defaultConfigPath() string {
	return `C:\ProgramData\MDBService\config.yaml`
}

func generateKey() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		fatalf("generate key: %v", err)
	}
	return hex.EncodeToString(b)
}

func appendKeyToConfig(path, key string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	// Find the keys list and append.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "keys:" || strings.HasPrefix(trimmed, "keys:") {
			indent := strings.Repeat(" ", strings.Index(line, "k")+2)
			newLine := fmt.Sprintf(`%s- "%s"`, indent, key)
			lines = append(lines[:i+1], append([]string{newLine}, lines[i+1:]...)...)
			return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0600)
		}
	}
	return fmt.Errorf("could not find 'keys:' in config — add the key manually:\n    - %q", key)
}

func checkODBCDriver() error {
	// Try to open a connection with an empty DBQ to verify driver existence.
	// We expect an error about the file, not about the driver itself.
	_, err := mdb.ProbeDriver()
	return err
}

func testDatabase(path, driver string) (status string, tables string) {
	pool, err := mdb.NewPool([]mdb.DBConfig{{Alias: "test", Path: path, Driver: driver}})
	if err != nil {
		return fmt.Sprintf("✗ %v", err), "-"
	}
	defer pool.Close()

	db, _ := pool.Get("test")
	tbls, err := mdb.ListTables(context.Background(), db)
	if err != nil {
		return "✓ connected", fmt.Sprintf("✗ list tables: %v", err)
	}

	sort.Strings(tbls)
	return "✓ connected", fmt.Sprintf("%d (%s)", len(tbls), strings.Join(tbls, ", "))
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func printUsage() {
	fmt.Printf(`mdbapi %s — Microsoft Access REST API service

Usage:
  mdbapi <command> [flags]

Commands:
  run        [-config path]      Run in foreground (dev mode)
  install    [-config path]      Install Windows service
  uninstall  [-config path]      Uninstall Windows service
  start      [-config path]      Start the service
  stop       [-config path]      Stop the service
  check      [-config path]      Validate config and test DB connections
  keygen     [-add-to-config]    Generate a new API key
  fixacl     [-dir path]         Set restrictive ACLs on config directory
  version                        Print version

Default config path: C:\ProgramData\MDBService\config.yaml
`, Version)
}
