//go:build windows

package mdb

import (
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var odbcDLL = windows.NewLazySystemDLL("odbc32.dll")

var (
	procSQLAllocHandle    = odbcDLL.NewProc("SQLAllocHandle")
	procSQLSetEnvAttr     = odbcDLL.NewProc("SQLSetEnvAttr")
	procSQLDriverConnectW = odbcDLL.NewProc("SQLDriverConnectW")
	procSQLTablesW        = odbcDLL.NewProc("SQLTablesW")
	procSQLFetch          = odbcDLL.NewProc("SQLFetch")
	procSQLGetData        = odbcDLL.NewProc("SQLGetData")
	procSQLFreeHandle     = odbcDLL.NewProc("SQLFreeHandle")
	procSQLDisconnect     = odbcDLL.NewProc("SQLDisconnect")
)

// ODBC handle constants.
const (
	sqlHandleEnv    uintptr = 1
	sqlHandleDbc    uintptr = 2
	sqlHandleStmt   uintptr = 3
	sqlNullHandle   uintptr = 0
	sqlAttrOdbcVer  uintptr = 200
	sqlOvOdbc3      uintptr = 3
	sqlDriverNoPrompt uintptr = 0
	sqlSuccess      uintptr = 0
	sqlSuccessInfo  uintptr = 1
	sqlNoData       uintptr = 100
	// SQL_C_WCHAR = -8 as SQLSMALLINT; use ^uintptr(7) for portable sign extension.
	sqlCWchar = ^uintptr(7)  // 0xFFF...F8
	// SQL_NTS = -3 as SQLSMALLINT.
	sqlNTS = ^uintptr(2)  // 0xFFF...FD
	// TABLE_NAME is column 3 in the SQLTables result set (1-indexed).
	sqlTablesTableNameCol uintptr = 3
)

// listTablesViaODBCSyscall calls the ODBC SQLTablesW catalog function directly
// using the Win32 API, completely bypassing database/sql and MSysObjects.
// This is the most reliable table-listing method for ACE 2016+ where system
// table access via SQL is restricted regardless of credentials.
func listTablesViaODBCSyscall(ctx context.Context, connStr string) ([]string, error) {
	connStrW, err := windows.UTF16PtrFromString(connStr)
	if err != nil {
		return nil, fmt.Errorf("utf16 connstr: %w", err)
	}
	connStrLen := uintptr(len([]rune(connStr)))

	// "TABLE" filter — UTF-16, null terminated.
	tableTypeW, _ := windows.UTF16PtrFromString("TABLE")

	// 1. Environment handle.
	var hEnv uintptr
	if rc := sqlCall(procSQLAllocHandle, sqlHandleEnv, sqlNullHandle, uintptr(unsafe.Pointer(&hEnv))); !sqlOK(rc) {
		return nil, fmt.Errorf("SQLAllocHandle(ENV): rc=%d", rc)
	}
	defer procSQLFreeHandle.Call(sqlHandleEnv, hEnv) //nolint:errcheck

	// 2. Set ODBC version to 3.
	if rc := sqlCall(procSQLSetEnvAttr, hEnv, sqlAttrOdbcVer, sqlOvOdbc3, 0); !sqlOK(rc) {
		return nil, fmt.Errorf("SQLSetEnvAttr(ODBC_VERSION): rc=%d", rc)
	}

	// 3. Connection handle.
	var hDBC uintptr
	if rc := sqlCall(procSQLAllocHandle, sqlHandleDbc, hEnv, uintptr(unsafe.Pointer(&hDBC))); !sqlOK(rc) {
		return nil, fmt.Errorf("SQLAllocHandle(DBC): rc=%d", rc)
	}
	defer func() {
		procSQLDisconnect.Call(hDBC)       //nolint:errcheck
		procSQLFreeHandle.Call(sqlHandleDbc, hDBC) //nolint:errcheck
	}()

	// 4. Connect.
	if rc := sqlCall(procSQLDriverConnectW,
		hDBC, 0,
		uintptr(unsafe.Pointer(connStrW)), connStrLen,
		0, 0, 0,
		sqlDriverNoPrompt,
	); !sqlOK(rc) {
		return nil, fmt.Errorf("SQLDriverConnectW: rc=%d", rc)
	}

	// 5. Statement handle.
	var hStmt uintptr
	if rc := sqlCall(procSQLAllocHandle, sqlHandleStmt, hDBC, uintptr(unsafe.Pointer(&hStmt))); !sqlOK(rc) {
		return nil, fmt.Errorf("SQLAllocHandle(STMT): rc=%d", rc)
	}
	defer procSQLFreeHandle.Call(sqlHandleStmt, hStmt) //nolint:errcheck

	// 6. SQLTablesW(stmt, NULL, 0, NULL, 0, NULL, 0, "TABLE", SQL_NTS)
	if rc := sqlCall(procSQLTablesW,
		hStmt,
		0, 0, // catalog: NULL, len 0
		0, 0, // schema:  NULL, len 0
		0, 0, // table:   NULL, len 0
		uintptr(unsafe.Pointer(tableTypeW)), sqlNTS,
	); !sqlOK(rc) {
		return nil, fmt.Errorf("SQLTablesW: rc=%d", rc)
	}

	// 7. Fetch loop — TABLE_NAME is column 3.
	const bufChars = 256
	buf := make([]uint16, bufChars)
	var tables []string

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		rc := sqlCall(procSQLFetch, hStmt)
		if rc == sqlNoData {
			break
		}
		if !sqlOK(rc) {
			break
		}

		var lenOrInd int64
		rc = sqlCall(procSQLGetData,
			hStmt,
			sqlTablesTableNameCol,
			sqlCWchar,
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(bufChars*2), // buffer size in bytes
			uintptr(unsafe.Pointer(&lenOrInd)),
		)
		if sqlOK(rc) && lenOrInd > 0 {
			tables = append(tables, windows.UTF16ToString(buf))
		}
	}

	return tables, nil
}

func sqlCall(proc *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := proc.Call(args...)
	// SQLRETURN is a 16-bit signed integer. On 32-bit Windows the upper
	// bits of the uintptr return value may contain garbage.
	return r & 0xFFFF
}

func sqlOK(rc uintptr) bool {
	return rc == sqlSuccess || rc == sqlSuccessInfo
}
