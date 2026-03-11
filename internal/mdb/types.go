// Package mdb handles Microsoft Access database access via ODBC.
package mdb

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// NormalizeValue converts a raw value returned by alexbrainman/odbc into a
// JSON-safe Go value using the following contract:
//
//   - Boolean (Yes/No):    int16 -1/0  → true/false
//   - Integers:            int32/int64 → int64
//   - Float/Double:        float32     → float64
//   - Currency:            float64     (passed through; 4 decimal precision)
//   - Date/Time:           time.Time   → RFC3339 string (UTC)
//   - Text/Memo:           string      (passed through; memo may be truncated at ~32KB by driver)
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
