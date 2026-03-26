package mdb

import (
	"database/sql"
	"testing"
)

func TestPool_Register_NewAlias(t *testing.T) {
	p := &Pool{
		dbs:      make(map[string]*sql.DB),
		paths:    make(map[string]string),
		connStrs: make(map[string]string),
	}

	p.paths["existing"] = `C:\data\existing.mdb`
	p.dbs["existing"] = nil
	p.connStrs["existing"] = "fake"

	if got := len(p.Aliases()); got != 1 {
		t.Fatalf("aliases = %d, want 1", got)
	}
}

func TestPool_Register_Dedup(t *testing.T) {
	p := &Pool{
		dbs:      make(map[string]*sql.DB),
		paths:    make(map[string]string),
		connStrs: make(map[string]string),
	}
	p.dbs["mydb"] = nil
	p.paths["mydb"] = `C:\data\mydb.mdb`
	p.connStrs["mydb"] = "fake"

	err := p.Register(DBConfig{Alias: "mydb", Path: `C:\data\mydb.mdb`})
	if err != nil {
		t.Fatalf("Register dedup returned error: %v", err)
	}
	if got := len(p.Aliases()); got != 1 {
		t.Errorf("aliases after dedup = %d, want 1", got)
	}
}
