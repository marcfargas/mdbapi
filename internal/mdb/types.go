// Package mdb handles Microsoft Access database access via ODBC.
package mdb

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// NormalizeValue converts a raw value returned by alexbrainman/odbc into a
// JSON-safe Go value using the following contract:
//
//   - Boolean (Yes/No):    int16 -1/0  → true/false
//   - Integers:            int32/int64 → int64
//   - Float/Double:        float32     → float64
//   - Currency:            float64     (passed through; 4 decimal precision)
//   - Date/Time:           time.Time   → RFC3339 string (UTC)
//   - Text/Memo:           string/[]byte → string ([]byte text columns converted)
//   - OLE Object/Binary:   []byte      → base64 string (or null if nil)
//   - Hyperlink:           string      → stripped of Access # delimiter metadata
//   - Nil:                 nil         (JSON null)
func NormalizeValue(col *sql.ColumnType, val interface{}) interface{} {
	if val == nil {
		return nil
	}

	switch v := val.(type) {
	case bool:
		return v

	// Access Yes/No fields come as int16: -1 = true, 0 = false.
	case int16:
		return v != 0

	case int32:
		return int64(v)

	case int64:
		return v

	case float32:
		return float64(v)

	case float64:
		return v

	case string:
		if col != nil && isHyperlinkColumn(col) {
			return stripHyperlinkMeta(v)
		}
		return v

	case []byte:
		if len(v) == 0 {
			return nil
		}
		// Text/memo columns arrive as []byte via ODBC — convert to string.
		// Only base64-encode actual binary columns (OLE Object).
		//
		// Strategy: first check ODBC type name if available, then fall back
		// to UTF-8 validity. alexbrainman/odbc does not implement
		// DatabaseTypeName() (returns ""), so the fallback is the primary
		// path. Real binary/OLE data is almost never valid UTF-8.
		if col != nil && isTextColumn(col) {
			return string(v)
		}
		if utf8.Valid(v) {
			return string(v)
		}
		return base64.StdEncoding.EncodeToString(v)

	case time.Time:
		if v.IsZero() {
			return nil
		}
		return v.UTC().Format(time.RFC3339)

	default:
		// Fallback: stringify unknown types.
		return fmt.Sprintf("%v", v)
	}
}

// isTextColumn returns true when the ODBC column type indicates a text field.
// Access text and memo columns are reported as VARCHAR/LONGVARCHAR (ANSI) or
// WVARCHAR/WLONGVARCHAR (Unicode) by the ACE ODBC driver, but the Go driver
// may deliver their values as []byte rather than string.
func isTextColumn(col *sql.ColumnType) bool {
	return isTextTypeName(col.DatabaseTypeName())
}

// isTextTypeName returns true for ODBC type names that represent text fields.
func isTextTypeName(typeName string) bool {
	switch strings.ToUpper(typeName) {
	case "VARCHAR", "LONGVARCHAR", "WVARCHAR", "WLONGVARCHAR",
		"CHAR", "WCHAR", "NVARCHAR", "NCHAR", "TEXT", "NTEXT":
		return true
	}
	return false
}

// isHyperlinkColumn returns true when the column type name suggests a hyperlink.
// Access stores hyperlinks as text; the driver reports them as VARCHAR/LONGVARCHAR.
// We rely on column name heuristic or caller annotation — conservative by default.
func isHyperlinkColumn(col *sql.ColumnType) bool {
	// Access doesn't expose hyperlink type via ODBC metadata reliably.
	// Columns named "*_link", "*_url", "*_hyperlink" are treated as hyperlinks.
	lower := strings.ToLower(col.Name())
	return strings.HasSuffix(lower, "_link") ||
		strings.HasSuffix(lower, "_url") ||
		strings.HasSuffix(lower, "_hyperlink")
}

// stripHyperlinkMeta removes Access hyperlink metadata from the format:
// "displaytext#address#screenTip" → "address" (the actual URL/path).
func stripHyperlinkMeta(s string) string {
	parts := strings.SplitN(s, "#", 3)
	switch len(parts) {
	case 1:
		return s
	case 2:
		if parts[1] != "" {
			return parts[1]
		}
		return parts[0]
	default: // 3+
		if parts[1] != "" {
			return parts[1]
		}
		return parts[0]
	}
}
