package database

import (
	"os"
	"testing"
)

func TestInitDB(t *testing.T) {
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	db, err := InitDB(tmp.Name())
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	var fkEnabled int
	err = db.QueryRow("PRAGMA foreign_keys").Scan(&fkEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if fkEnabled != 1 {
		t.Errorf("foreign_keys = %d, want 1", fkEnabled)
	}

	tables := []string{"users", "series", "chapters", "provider_configs", "sessions"}
	for _, table := range tables {
		var count int
		err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("table %q not found", table)
		}
	}
}

func TestInitDB_Idempotent(t *testing.T) {
	tmp, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	db1, err := InitDB(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	db1.Close()

	db2, err := InitDB(tmp.Name())
	if err != nil {
		t.Fatalf("second InitDB failed: %v", err)
	}
	db2.Close()
}
