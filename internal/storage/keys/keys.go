// Package keys constructs every Pebble key in go-parts. Single source for the
// keyspace. Shape: kind(1) | ws(8) | payload. ws is fixed to zero in v1
// (single-vault); reserved for Phase-8 multi-vault.
package keys

const (
	partsPrefix          byte = 0x10
	locationPrefix       byte = 0x11 // Location records (nested storage)
	metaPrefix           byte = 0xF0
	viaPrefix            byte = 0x12 // via-code index (code string → {type,id}); §5.17
	componentPrefix      byte = 0x13 // Component records (Part-at-Location junction); §6.1
	identPrefix          byte = 0x14 // part-identity uniqueness index (kind|ws|value → partID); MPN + LocalNumber
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

func viaKey(ws [8]byte, code string) []byte {
	return scopedString(viaPrefix, ws, code)
}

// ViaKey is the exported alias for viaKey, for internal/via.
func ViaKey(ws [8]byte, code string) []byte { return viaKey(ws, code) }

// IdentKind is the sub-kind byte distinguishing which part-identity value an
// 0x14 index entry pins: MPN or LocalNumber. One prefix, two kinds — a future
// identity kind adds a byte, not a keyspace.
type IdentKind byte

const (
	IdentMPN   IdentKind = 'M'
	IdentLocal IdentKind = 'L'
)

// IdentIndexKey returns the part-identity uniqueness-index key:
// kind(1) | ws(8) | value. Mirrors viaKey's shape (a value index pointing at
// the owning part's ID), with the kind byte where via has its prefix.
func IdentIndexKey(ws [8]byte, kind IdentKind, value string) []byte {
	k := make([]byte, 0, 1+8+len(value))
	k = append(k, byte(kind))
	k = append(k, ws[:]...)
	return append(k, value...)
}

// IdentPrefixBound returns the [lower, upper) range covering every 0x14
// identity-index entry, for prefix scans/tests. 0x14+1 = 0x15, free (same
// < 0xFF caveat as PartsPrefixBound).
func IdentPrefixBound(ws [8]byte) (lower, upper []byte) {
	lower = scopedBytes(identPrefix, ws)
	upper = scopedBytes(identPrefix+1, ws)
	return lower, upper
}

// ViaPrefixBound returns the [lower, upper) range covering every via-code
// index entry, for prefix scans/tests. viaPrefix+1 relies on viaPrefix (0x12)
// being < 0xFF (same caveat as PartsPrefixBound).
func ViaPrefixBound(ws [8]byte) (lower, upper []byte) {
	lower = scopedBytes(viaPrefix, ws)
	upper = scopedBytes(viaPrefix+1, ws)
	return lower, upper
}

func locationKey(ws [8]byte, id string) []byte {
	return scopedString(locationPrefix, ws, id)
}

// LocationKey is the exported alias for locationKey, for internal/locations.
func LocationKey(ws [8]byte, id string) []byte { return locationKey(ws, id) }

// LocationPrefixBound returns the [lower, upper) range covering every location
// record, for prefix scans (List/Count/Children). locationPrefix+1 (0x12) is
// the via prefix, NOT a free byte — but the bound is [0x11, 0x12) which covers
// ONLY 0x11 keys; it never overlaps 0x12 because the upper bound is exclusive.
// (Same < 0xFF caveat as PartsPrefixBound; 0x11+1 = 0x12, no wrap.)
func LocationPrefixBound(ws [8]byte) (lower, upper []byte) {
	lower = scopedBytes(locationPrefix, ws)
	upper = scopedBytes(locationPrefix+1, ws)
	return lower, upper
}

// componentKey builds the composite key for a Component (Part-at-Location
// junction, §6.1): kind(1) | ws(8) | locID | partID. locID and partID are the
// ULID strings (26 bytes each) issued by locations/parts respectively; the
// pair (locID, partID) is the Component uniqueness boundary — one record per
// part per location.
//
// Key ordering: with locID as the high-order payload, a prefix scan over
// `kind|ws|locID` returns every Component at one location (the
// "what's in this bin?" query); a per-part lookup across locations is a
// scan-and-filter, not a single get (v1 YAGNI; an inverse index lands only if
// the makerspace scale demands it). Callers needing the per-location bound
// build it inline: LowerBound = append(scopedBytes(componentPrefix, ws), locID...).
func componentKey(ws [8]byte, locID, partID string) []byte {
	b := scopedBytes(componentPrefix, ws)
	b = append(b, locID...)
	b = append(b, partID...)
	return b
}

// ComponentKey is the exported alias for componentKey, for internal/components.
func ComponentKey(ws [8]byte, locID, partID string) []byte {
	return componentKey(ws, locID, partID)
}

// ComponentPrefixBound returns the [lower, upper) range covering every
// component record, for prefix scans (List/Count over the whole keyspace).
// componentPrefix+1 (0x14) is currently free — the bound is [0x13, 0x14) and
// never overlaps an in-use prefix. (Same < 0xFF caveat as PartsPrefixBound:
// 0x13+1 = 0x14, no wrap.) For the per-location scan shape, see componentKey.
func ComponentPrefixBound(ws [8]byte) (lower, upper []byte) {
	lower = scopedBytes(componentPrefix, ws)
	upper = scopedBytes(componentPrefix+1, ws)
	return lower, upper
}

// ComponentLocationPrefixBound returns the [lower, upper) range covering every
// Component at one location — the "what's in this bin?" query (§6.1). Lower is
// kind|ws|locID; upper is that plus a 0xFF sentinel — strictly greater than any
// partID byte (ULID characters from Crockford Base32, all ASCII ≤ 0x5A, all
// < 0xFF), so the exclusive upper bound captures every component under locID
// without overlapping the next location's range.
func ComponentLocationPrefixBound(ws [8]byte, locID string) (lower, upper []byte) {
	lower = ComponentKey(ws, locID, "")
	upper = append(append([]byte{}, lower...), 0xFF)
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
