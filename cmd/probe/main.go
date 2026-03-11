//go:build windows

// probe is the Phase 0 ODBC validation tool.
// It opens MDB/ACCDB files and reports the Go type of every column value,
// validating that the Access ODBC driver returns sensible types for booleans,
// dates, integers, and binary fields.
//
// Usage:
//
//	probe <path.mdb> [path2.accdb ...]
//	probe -dir <testdata-dir>
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/alexbrainman/odbc"
	"github.com/marcfargas/mdbapi/internal/mdb"
)

func main() {
	dir := flag.String("dir", "", "directory to scan for .mdb/.accdb files")
	maxRows := flag.Int("rows", 3, "sample rows per table")
	flag.Parse()

	var files []string
	if *dir != "" {
		var err error
		files, err = findMDBFiles(*dir)
		if err != nil {
			fatalf("scan dir: %v", err)
		}
		if len(files) == 0 {
			fatalf("no .mdb or .accdb files found in %s", *dir)
		}
	} else {
		files = flag.Args()
	}

	if len(files) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: probe [-dir <dir>] [file.mdb ...]\n")
		os.Exit(1)
	}

	// 1. Driver probe.
	fmt.Println("=== ODBC Driver Check ===")
	msg, err := mdb.ProbeDriver()
	if err != nil {
		fmt.Printf("✗ %v\n\n", err)
	} else {
		fmt.Printf("✓ %s\n\n", msg)
	}

	// 2. Per-file analysis.
	for _, f := range files {
		probeFile(f, *maxRows)
	}
}

func probeFile(path string, maxRows int) {
	fmt.Printf("=== %s ===\n", path)
	db, err := openReadOnly(path)
	if err != nil {
		fmt.Printf("✗ open: %v\n\n", err)
		return
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Table listing: try MSysObjects first, then fallback.
	tables, msysErr := listTablesViaMSys(ctx, db)
	if msysErr != nil {
		fmt.Printf("  MSysObjects: ✗ %v (will use fallback)\n", msysErr)
		tables, err = listTablesViaFallback(ctx, db)
		if err != nil {
			fmt.Printf("  Fallback table list: ✗ %v\n\n", err)
			return
		}
		fmt.Printf("  MSysObjects fallback: ✓ %d tables\n", len(tables))
	} else {
		fmt.Printf("  MSysObjects: ✓ %d tables\n", len(tables))
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, table := range tables {
		probeTable(ctx, db, table, maxRows, w)
	}
	_ = w.Flush()
	fmt.Println()
}

func probeTable(ctx context.Context, db *sql.DB, table string, maxRows int, w *tabwriter.Writer) {
	fmt.Fprintf(w, "\n  [%s]\n", table)
	fmt.Fprintf(w, "  COLUMN\tDB TYPE\tGO TYPE\tSAMPLE VALUE\tNORMALIZED\n")
	fmt.Fprintf(w, "  ------\t-------\t-------\t------------\t----------\n")

	query := fmt.Sprintf("SELECT TOP %d * FROM [%s]", maxRows, table)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		fmt.Fprintf(w, "  ✗ query error: %v\n", err)
		return
	}
	defer rows.Close()

	colTypes, _ := rows.ColumnTypes()
	colNames, _ := rows.Columns()

	// Print column schema.
	for _, ct := range colTypes {
		nullable, _ := ct.Nullable()
		nullStr := ""
		if nullable {
			nullStr = " (nullable)"
		}
		fmt.Fprintf(w, "  %s\t%s%s\t-\t-\t-\n", ct.Name(), ct.DatabaseTypeName(), nullStr)
	}

	// Print sample rows.
	rowCount := 0
	for rows.Next() {
		scanDest := make([]interface{}, len(colNames))
		for i := range scanDest {
			scanDest[i] = new(interface{})
		}
		if err := rows.Scan(scanDest...); err != nil {
			fmt.Fprintf(w, "  ✗ scan: %v\n", err)
			continue
		}
		rowCount++
		for i, name := range colNames {
			raw := *(scanDest[i].(*interface{}))
			goType := reflect.TypeOf(raw)
			normalized := mdb.NormalizeValue(colTypes[i], raw)
			sample := formatSample(raw)
			normStr := formatSample(normalized)
			fmt.Fprintf(w, "  %s [row%d]\t-\t%v\t%s\t%s\n",
				name, rowCount, goType, sample, normStr)
		}
	}
	if rowCount == 0 {
		fmt.Fprintf(w, "  (no rows)\n")
	}
}

func formatSample(v interface{}) string {
	if v == nil {
		return "NULL"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

func openReadOnly(path string) (*sql.DB, error) {
	connStr := fmt.Sprintf(
		`DRIVER={Microsoft Access Driver (*.mdb, *.accdb)};DBQ=%s;ReadOnly=1;`,
		path,
	)
	db, err := sql.Open("odbc", connStr)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func listTablesViaMSys(ctx context.Context, db *sql.DB) ([]string, error) {
	const q = `SELECT Name FROM MSysObjects WHERE (Type In (1,4)) AND (Flags In (0,-2147483648)) ORDER BY Name`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func listTablesViaFallback(ctx context.Context, db *sql.DB) ([]string, error) {
	// Try the ODBC schema query approach.
	rows, err := db.QueryContext(ctx,
		`SELECT MSysObjects.Name FROM MSysObjects WHERE MSysObjects.Type=1 AND MSysObjects.Flags=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func findMDBFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		lower := strings.ToLower(path)
		if strings.HasSuffix(lower, ".mdb") || strings.HasSuffix(lower, ".accdb") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
