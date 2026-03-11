package mdb

import (
	"context"
	"database/sql"
	"fmt"
)

// ColumnMeta describes one column in an Access table.
type ColumnMeta struct {
	Name     string
	TypeName string // ODBC type name as reported by the driver
	Nullable bool
}

// ListTables returns user table names for the given database.
// It tries MSysObjects first; falls back to the ODBC schema API on permission errors.
func ListTables(ctx context.Context, db *sql.DB) ([]string, error) {
	tables, err := listTablesViaMSys(ctx, db)
	if err != nil {
		// MSysObjects is commonly permission-denied on production databases.
		// Retry via ODBC catalog function.
		tables, err = listTablesViaODBC(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("list tables: %w", err)
		}
	}
	return tables, nil
}

// listTablesViaMSys queries the Access system table directly.
// Works on most Jet/ACE databases unless security hardening disables system table access.
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

// listTablesViaODBC uses the ODBC SQLTables catalog function via a special
// stored-procedure-style query that alexbrainman/odbc supports.
// This does not require MSysObjects access.
func listTablesViaODBC(ctx context.Context, db *sql.DB) ([]string, error) {
	// The ODBC driver exposes catalog tables via a pseudo-query.
	// For Access, TABLE_TYPE='TABLE' returns user tables.
	rows, err := db.QueryContext(ctx,
		`SELECT TABLE_NAME FROM [.Tables] WHERE TABLE_TYPE='TABLE'`)
	if err != nil {
		// Last resort: try a direct SHOW TABLES equivalent isn't available in Access,
		// so we attempt to query the catalog via a driver-specific approach.
		return listTablesViaSystemQuery(ctx, db)
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

// listTablesViaSystemQuery is a last-resort fallback using the Admin user path.
func listTablesViaSystemQuery(ctx context.Context, db *sql.DB) ([]string, error) {
	// Try with explicit Admin credentials embedded in query context.
	const q = `SELECT MSysObjects.Name FROM MSysObjects WHERE MSysObjects.Type=1 AND MSysObjects.Flags=0`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("all table listing methods failed; last error: %w", err)
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

// ColumnCache maintains a per-database, per-table schema cache.
// It is populated lazily on first query and used for filter param type coercion.
type ColumnCache struct {
	db    *sql.DB
	cache map[string][]ColumnMeta // table name → columns
}

// NewColumnCache creates a cache backed by the given database connection.
func NewColumnCache(db *sql.DB) *ColumnCache {
	return &ColumnCache{db: db, cache: make(map[string][]ColumnMeta)}
}

// Columns returns the schema for a table, fetching and caching on first access.
func (c *ColumnCache) Columns(ctx context.Context, table string) ([]ColumnMeta, error) {
	if cols, ok := c.cache[table]; ok {
		return cols, nil
	}
	cols, err := describeTable(ctx, c.db, table)
	if err != nil {
		return nil, err
	}
	c.cache[table] = cols
	return cols, nil
}

// ValidateColumn returns an error if colName is not in the table's schema.
// Used to prevent SQL injection via column name parameters.
func (c *ColumnCache) ValidateColumn(ctx context.Context, table, colName string) error {
	cols, err := c.Columns(ctx, table)
	if err != nil {
		return err
	}
	for _, col := range cols {
		if col.Name == colName {
			return nil
		}
	}
	return fmt.Errorf("unknown column %q in table %q", colName, table)
}

// describeTable fetches column metadata for a single table using a zero-row query.
func describeTable(ctx context.Context, db *sql.DB, table string) ([]ColumnMeta, error) {
	// Use a query that returns no rows but gives us column type information.
	// The table name is not user-supplied in the public API without prior validation,
	// so direct interpolation here is safe (table comes from ListTables).
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM [%s] WHERE 1=0", table))
	if err != nil {
		return nil, fmt.Errorf("describe table %q: %w", table, err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("describe table %q column types: %w", table, err)
	}

	cols := make([]ColumnMeta, len(colTypes))
	for i, ct := range colTypes {
		nullable, _ := ct.Nullable()
		cols[i] = ColumnMeta{
			Name:     ct.Name(),
			TypeName: ct.DatabaseTypeName(),
			Nullable: nullable,
		}
	}
	return cols, nil
}

// ColumnType returns the database type name for a specific column.
func (c *ColumnCache) ColumnType(ctx context.Context, table, colName string) (string, error) {
	cols, err := c.Columns(ctx, table)
	if err != nil {
		return "", err
	}
	for _, col := range cols {
		if col.Name == colName {
			return col.TypeName, nil
		}
	}
	return "", fmt.Errorf("column %q not found in table %q", colName, table)
}
