package database

import "testing"

func TestDB(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	if db == nil {
		t.Fatal("DB is nil")
	}
}
