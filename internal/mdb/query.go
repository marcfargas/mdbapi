package mdb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// QueryOpts controls row-level query behaviour.
type QueryOpts struct {
	// Filters maps column name → value string (from URL query params).
	Filters map[string]string
	// Limit caps the result set. Defaults to pool MaxRows if 0.
	Limit int
	// Offset is the row skip count for pagination.
	Offset int
	// SortBy is an optional column to ORDER BY (must be in schema).
	SortBy string
	// SortDesc reverses sort order when true.
	SortDesc bool
}

// QueryResult holds rows and the column names used to build JSON keys.
type QueryResult struct {
	Columns []string
	Rows    []map[string]interface{}
}

// QueryTable executes a filtered, paginated SELECT against the named table.
// Column names from filters are validated against the schema cache to prevent
// SQL injection via identifier manipulation.
func QueryTable(
	ctx context.Context,
	db *sql.DB,
	cache *ColumnCache,
	table string,
	opts QueryOpts,
	maxRows int,
) (*QueryResult, error) {
	if opts.Limit <= 0 || opts.Limit > maxRows {
		opts.Limit = maxRows
	}

	// Build WHERE clause from filters, validating each column name.
	var (
		whereClauses []string
		args         []interface{}
	)
	for col, val := range opts.Filters {
		if err := cache.ValidateColumn(ctx, table, col); err != nil {
			return nil, fmt.Errorf("invalid filter: %w", err)
		}
		whereClauses = append(whereClauses, fmt.Sprintf("[%s] = ?", col))
		args = append(args, val)
	}

	// Build ORDER BY clause.
	var orderClause string
	if opts.SortBy != "" {
		if err := cache.ValidateColumn(ctx, table, opts.SortBy); err != nil {
			return nil, fmt.Errorf("invalid sort column: %w", err)
		}
		dir := "ASC"
		if opts.SortDesc {
			dir = "DESC"
		}
		orderClause = fmt.Sprintf(" ORDER BY [%s] %s", opts.SortBy, dir)
	}

	// Access uses TOP N instead of LIMIT/OFFSET.
	// For offset support we fetch limit+offset and skip the first offset rows.
	// This is acceptable given the conservative row limits in use.
	fetchN := opts.Limit + opts.Offset

	query := fmt.Sprintf("SELECT TOP %d * FROM [%s]", fetchN, table)
	if len(whereClauses) > 0 {
		query += " WHERE " + strings.Join(whereClauses, " AND ")
	}
	query += orderClause

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query table %q: %w", table, err)
	}
	defer rows.Close()

	return scanRows(rows, opts.Offset)
}

// ExecuteSQL runs a raw SQL statement.
// Only SELECT and WITH (CTEs) are allowed; anything else returns ErrWriteNotAllowed.
func ExecuteSQL(
	ctx context.Context,
	db *sql.DB,
	query string,
	params []interface{},
	maxRows int,
) (*QueryResult, error) {
	if err := validateReadOnlySQL(query); err != nil {
		return nil, err
	}

	// Enforce row cap by wrapping in TOP N if no TOP is already present.
	wrapped := enforceTopN(query, maxRows)

	rows, err := db.QueryContext(ctx, wrapped, params...)
	if err != nil {
		return nil, fmt.Errorf("execute SQL: %w", err)
	}
	defer rows.Close()

	return scanRows(rows, 0)
}

// ParseQueryOpts extracts QueryOpts from URL query parameters.
// Reserved params (limit, offset, sort, sort_desc) are consumed; all others
// are treated as column filters.
func ParseQueryOpts(q url.Values) QueryOpts {
	opts := QueryOpts{Filters: make(map[string]string)}
	reserved := map[string]bool{"limit": true, "offset": true, "sort": true, "sort_desc": true}

	for key, vals := range q {
		if reserved[key] || len(vals) == 0 {
			continue
		}
		opts.Filters[key] = vals[0]
	}

	if v := q.Get("limit"); v != "" {
		fmt.Sscan(v, &opts.Limit)
	}
	if v := q.Get("offset"); v != "" {
		fmt.Sscan(v, &opts.Offset)
	}
	opts.SortBy = q.Get("sort")
	opts.SortDesc = strings.EqualFold(q.Get("sort_desc"), "true") ||
		q.Get("sort_desc") == "1"

	return opts
}

// scanRows reads all rows from a sql.Rows into a QueryResult, skipping the
// first skipN rows (for offset emulation).
func scanRows(rows *sql.Rows, skipN int) (*QueryResult, error) {
	colNames, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}

	result := &QueryResult{Columns: colNames}
	rowIdx := 0

	for rows.Next() {
		// Allocate a fresh slice of interface{} per row to receive scan values.
		scanDest := make([]interface{}, len(colNames))
		for i := range scanDest {
			scanDest[i] = new(interface{})
		}
		if err := rows.Scan(scanDest...); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		rowIdx++
		if rowIdx <= skipN {
			continue
		}

		rowMap := make(map[string]interface{}, len(colNames))
		for i, name := range colNames {
			raw := *(scanDest[i].(*interface{}))
			rowMap[name] = NormalizeValue(colTypes[i], raw)
		}
		result.Rows = append(result.Rows, rowMap)
	}
	return result, rows.Err()
}

// validateReadOnlySQL checks that only SELECT or WITH (CTE → SELECT) statements
// are submitted. Comments and leading whitespace are stripped before checking.
func validateReadOnlySQL(query string) error {
	normalized := stripSQLComments(strings.TrimSpace(query))
	upper := strings.ToUpper(normalized)
	if strings.HasPrefix(upper, "SELECT") || strings.HasPrefix(upper, "WITH") {
		return nil
	}
	return ErrWriteNotAllowed
}

// stripSQLComments removes -- line comments and /* */ block comments.
func stripSQLComments(s string) string {
	// Remove block comments.
	for {
		start := strings.Index(s, "/*")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "*/")
		if end == -1 {
			break
		}
		s = s[:start] + " " + s[start+end+2:]
	}
	// Remove line comments.
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			lines[i] = line[:idx]
		}
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

// enforceTopN wraps the query with TOP N if not already present.
// Access SQL does not support LIMIT; TOP N is applied at the SELECT level.
func enforceTopN(query string, n int) string {
	upper := strings.ToUpper(strings.TrimSpace(query))
	// If the query already has TOP, respect it (we can't override safely).
	if strings.Contains(upper, " TOP ") || strings.HasPrefix(upper, "SELECT TOP") {
		return query
	}
	if strings.HasPrefix(upper, "SELECT") {
		return "SELECT TOP " + fmt.Sprintf("%d", n) + query[6:]
	}
	return query
}

// ErrWriteNotAllowed is returned when a non-SELECT SQL statement is submitted.
var ErrWriteNotAllowed = fmt.Errorf("only SELECT statements are allowed")
