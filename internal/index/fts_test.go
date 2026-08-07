// Package index holds go-parts' full-text search indexes (PRD §5.1). This file
// holds the TDD test that pins the BM25 port (task 7) — written first (RED),
// satisfied by fts.go (GREEN).
package index

import (
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble"
)

func newDB(t *testing.T) *pebble.DB {
	t.Helper()
	db, err := pebble.Open(filepath.Join(t.TempDir(), "p"), &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestIndexSearchRoundTrip(t *testing.T) {
	var ws [8]byte
	f := NewFTS(newDB(t))
	f.Index(ws, "p1", map[string]string{
		"mpn":         "RC0805FR-0710KL",
		"description": "10k resistor 0805",
		"tags":        "resistor smd",
	})
	hits := f.Search(ws, "10k resistor", 5)
	if len(hits) != 1 || hits[0].ID != "p1" {
		t.Fatalf("hits = %+v, want p1", hits)
	}
}

func TestPrefixExpansion(t *testing.T) {
	var ws [8]byte
	f := NewFTS(newDB(t))
	f.Index(ws, "p1", map[string]string{"mpn": "STM32F4"})
	hits := f.Search(ws, "STM", 5) // term <4 chars → prefix expansion
	if len(hits) != 1 {
		t.Fatalf("prefix hits = %d, want 1", len(hits))
	}
}

func TestDeleteRemovesFromIndex(t *testing.T) {
	var ws [8]byte
	f := NewFTS(newDB(t))
	f.Index(ws, "p1", map[string]string{"description": "capacitor 100nF"})
	f.Delete(ws, "p1", "capacitor 100nF")
	if hits := f.Search(ws, "capacitor", 5); len(hits) != 0 {
		t.Fatalf("after delete, hits = %d, want 0", len(hits))
	}
}
