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

// ListTables returns user table names via MSysObjects.
// Used by cmd/probe for direct ODBC introspection.
// PoolStore.ListTables uses the ODBC catalog API (catalog_windows.go) instead,
// which works on all ACE versions without MSysObjects permissions.
func ListTables(ctx context.Context, db *sql.DB) ([]string, error) {
	const q = `SELECT Name FROM MSysObjects ` +
		`WHERE (Type In (1,4)) AND (Flags In (0,-2147483648)) ` +
		`ORDER BY Name`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list tables (MSysObjects): %w", err)
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
