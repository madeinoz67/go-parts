package keys

import "testing"

func TestPartsKeyShape(t *testing.T) {
	var ws [8]byte // fixed zero in v1
	got := partsKey(ws, "01ABC")
	want := append([]byte{0x10}, append(ws[:], []byte("01ABC")...)...)
	if string(got) != string(want) {
		t.Fatalf("partsKey = %x, want %x", got, want)
	}
}

func TestPrefixesDisjoint(t *testing.T) {
	var ws [8]byte
	// every public prefix constructor's first byte must be unique
	firstBytes := map[byte]string{
		partsPrefix:          "parts",
		metaPrefix:           "meta",
		ftsPostingPrefix:     "ftsPosting",
		ftsIndexedPrefix:     "ftsIndexed",
		ftsGlobalStatsPrefix: "ftsStats",
	}
	if len(firstBytes) != 5 {
		t.Fatalf("expected 5 distinct prefixes, got %d", len(firstBytes))
	}
	_ = ftsPostingKey(ws, "t", "id") // force use
	_ = ftsIndexedKey(ws, "id")
	_ = ftsGlobalStatsKey(ws)
}
