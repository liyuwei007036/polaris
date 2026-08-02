package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredDatabasePath(t *testing.T) {
	directory := t.TempDir()
	dataDirectory := filepath.Join(directory, "master-data")
	databasePath := filepath.Join(directory, "database", "control.db")
	store, err := OpenWithDatabase(dataDirectory, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(databasePath); err != nil {
		t.Fatalf("configured database was not created: %v", err)
	}
}
