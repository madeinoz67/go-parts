// Package keys constructs every Pebble key in go-parts. Single source for the
// keyspace. Shape: kind(1) | ws(8) | payload. ws is fixed to zero in v1
// (single-vault); reserved for Phase-8 multi-vault.
package keys

const (
	partsPrefix          byte = 0x10
	metaPrefix           byte = 0xF0
	ftsPostingPrefix     byte = 0x05 // verbatim from go-rag
	ftsIndexedPrefix     byte = 0x07 // verbatim from go-rag
	ftsGlobalStatsPrefix byte = 0x06 // verbatim from go-rag
)

func partsKey(ws [8]byte, id string) []byte {
	return scopedString(partsPrefix, ws, id)
}

// PartsKey is the exported alias for partsKey, for cross-package callers
// (e.g. internal/storage/migrate tests).
func PartsKey(ws [8]byte, id string) []byte { return partsKey(ws, id) }

// PartsPrefixBound returns the [lower, upper) key range covering every parts
// record, for prefix scans (e.g. Store.Count). Exported so callers outside the
// keys package can iterate without touching raw bytes.
func PartsPrefixBound(ws [8]byte) (lower, upper []byte) {
	lower = scopedBytes(partsPrefix, ws)
	// NOTE: partsPrefix+1 relies on partsPrefix (0x10) being < 0xFF; a future prefix of 0xFF would wrap uint8 to 0x00.
	upper = scopedBytes(partsPrefix+1, ws)
	return lower, upper
}

func metaSchemaVersionKey() []byte {
	var ws [8]byte
	return scopedString(metaPrefix, ws, "schemaver")
}

// MetaSchemaVersionKey is the exported alias for metaSchemaVersionKey, for
// cross-package callers (e.g. internal/storage/migrate pins the migration
// version key against the same bytes).
func MetaSchemaVersionKey() []byte { return metaSchemaVersionKey() }

// --- FTS (verbatim shape from go-rag internal/storage/keys) ---
//
// The four constructors mirror go-rag's FTS key shapes exactly so the ported
// internal/index/fts.go (BM25) reads/writes the same layout it was proven
// against. Three prefix bytes are go-parts' own allocation (keyspace-registry:
// 0x05/0x07/0x06) but the payload shapes are byte-identical to go-rag.
//
// ftsPostingTermPrefix deliberately returns kind|ws|term WITHOUT the 0x00
// terminator — the terminator belongs only on the full posting key. The BM25
// scanner pairs this prefix with an exclusive upper bound (kind|ws|term|0x01)
// to cover every chunkID under that term.

func ftsPostingKey(ws [8]byte, term, id string) []byte {
	// kind | ws | term | 0x00 | id
	b := scopedBytes(ftsPostingPrefix, ws)
	b = append(b, term...)
	b = append(b, 0x00)
	b = append(b, id...)
	return b
}

func ftsPostingTermPrefix(ws [8]byte, term string) []byte {
	// kind | ws | term  (NO terminator — see comment above)
	b := scopedBytes(ftsPostingPrefix, ws)
	b = append(b, term...)
	return b
}

func ftsIndexedKey(ws [8]byte, id string) []byte {
	return scopedString(ftsIndexedPrefix, ws, id)
}

func ftsGlobalStatsKey(ws [8]byte) []byte {
	return scopedBytes(ftsGlobalStatsPrefix, ws)
}

// Exported aliases for cross-package callers (internal/index). Same pattern as
// PartsKey/MetaSchemaVersionKey: the lowercase constructors stay canonical,
// these are the public seam.
func FTSPostingKey(ws [8]byte, term, id string) []byte    { return ftsPostingKey(ws, term, id) }
func FTSPostingTermPrefix(ws [8]byte, term string) []byte { return ftsPostingTermPrefix(ws, term) }
func FTSIndexedKey(ws [8]byte, id string) []byte          { return ftsIndexedKey(ws, id) }
func FTSGlobalStatsKey(ws [8]byte) []byte                 { return ftsGlobalStatsKey(ws) }

func scopedString(kind byte, ws [8]byte, payload string) []byte {
	b := scopedBytes(kind, ws)
	return append(b, payload...)
}

func scopedBytes(kind byte, ws [8]byte) []byte {
	b := make([]byte, 0, 1+8)
	b = append(b, kind)
	b = append(b, ws[:]...)
	return b
}
