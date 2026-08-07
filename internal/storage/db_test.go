package storage

import (
	"path/filepath"
	"testing"
)

func TestOpenAndClose(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "pebble"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.DB.Set([]byte("k"), []byte("v"), nil); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestSecondOpenerFails(t *testing.T) {
	dir := t.TempDir()
	s1, err := Open(filepath.Join(dir, "pebble"))
	if err != nil {
		t.Fatalf("Open s1: %v", err)
	}
	defer s1.Close()
	if _, err := Open(filepath.Join(dir, "pebble")); err == nil {
		t.Fatal("second Open succeeded; want flock failure")
	}
}
