// Package index holds go-parts' full-text search indexes (PRD §5.1): a Pebble-
// backed field-weighted BM25 index over part fields. Safe for concurrent use —
// the ingest pipeline's background workers mutate it alongside readers.
//
// This file is a near-verbatim port of go-rag/internal/index/fts.go (the BM25
// retrieval implementation proven across go-rag's retrieval audit + spec 018).
// The port is byte-faithful to go-rag's key-parse offsets and BM25 math; the
// only intentional changes are (a) the keys import path (go-rag → go-parts),
// (b) a chunkID→id rename that fits go-parts' part-centric vocabulary,
// (c) the fieldWeight map (go-parts fields, not document title/heading/body),
// and (d) the keys package delegation (go-parts owns its own 0x05/0x07/0x06
// prefix bytes per keyspace-registry). Everything else — k1/b constants, IDF
// cache, encodePosting/decodePosting, globalStats, prefix-expansion logic,
// atomic Pebble-batched Index/Delete, idempotency guard, MigrateFromChunks — is
// verbatim. See docs/internals/keyspace-registry.md for the FTS prefix bytes.
package index

import (
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/cockroachdb/pebble"
	"github.com/madeinoz67/go-parts/internal/storage/keys"
)

// BM25 constants (unchanged from go-rag — transparency over the retrieval math).
const (
	k1BM25 = 1.2
	bBM25  = 0.75
)

// Field-weight constants (go-parts). high fields (mpn/manufacturer/via_code)
// score 3x; mid fields (description/category/subcategory/footprint) score 2x;
// everything else (specs values, tags, custom_fields values) scores 1x.
const (
	weightHigh = 3.0
	weightMid  = 2.0
	weightBody = 1.0
)

// FTS is a Pebble-backed field-weighted BM25 full-text index. Postings live as
// Pebble keys under the FTSPosting prefix (0x05), queried via per-term prefix
// scans. The only in-memory state is a lazy IDF cache. Safe for concurrent use.
type FTS struct {
	db       *pebble.DB
	mu       sync.RWMutex
	idfCache map[string]float64 // string(ws[:]) + term → idf (invalidated on Index/Delete)
}

// NewFTS returns a Pebble-backed BM25 index over db. O(1) — no postings to load
// (they're on disk). The db must remain open for the FTS's lifetime.
func NewFTS(db *pebble.DB) *FTS {
	return &FTS{db: db, idfCache: make(map[string]float64, 1024)}
}

// fieldWeight returns the BM25 weight multiplier for a named part field.
func fieldWeight(field string) float64 {
	switch field {
	case "mpn", "manufacturer", "via_code":
		return weightHigh
	case "description", "category", "subcategory", "footprint":
		return weightMid
	default:
		return weightBody // specs values, tags, custom_fields values
	}
}

func idfCacheKey(ws [8]byte, term string) string {
	return string(ws[:]) + term
}

func postingTermUpperBound(ws [8]byte, term string) []byte {
	upper := keys.FTSPostingTermPrefix(ws, term)
	return append(upper, 0x01)
}

func prefixRangeUpperBound(lower []byte) []byte {
	upper := append([]byte(nil), lower...)
	last := len(upper) - 1
	upper[last]++
	if upper[last] == 0 {
		return nil
	}
	return upper
}

func (f *FTS) cachedIDF(ws [8]byte, term string, n, df float64) float64 {
	cacheKey := idfCacheKey(ws, term)

	f.mu.RLock()
	idf, ok := f.idfCache[cacheKey]
	f.mu.RUnlock()
	if ok {
		return idf
	}

	idf = math.Log(1 + (n-df+0.5)/(df+0.5))

	f.mu.Lock()
	defer f.mu.Unlock()
	if cached, ok := f.idfCache[cacheKey]; ok {
		return cached
	}
	f.idfCache[cacheKey] = idf
	return idf
}

// encodePosting encodes tf (weighted) + docLen into 6 bytes.
func encodePosting(tf float32, docLen int) []byte {
	buf := make([]byte, 6)
	binary.LittleEndian.PutUint32(buf[0:4], math.Float32bits(tf))
	binary.LittleEndian.PutUint16(buf[4:6], uint16(docLen))
	return buf
}

// decodePosting decodes 6 bytes into tf + docLen.
func decodePosting(buf []byte) (tf float32, docLen int) {
	if len(buf) < 6 {
		return 0, 0
	}
	tf = math.Float32frombits(binary.LittleEndian.Uint32(buf[0:4]))
	docLen = int(binary.LittleEndian.Uint16(buf[4:6]))
	return
}

// idFromKey extracts the id from a posting key (after the 0x00 sep).
func idFromKey(key []byte) string {
	// key = 0x05 | ws | term | 0x00 | id. Find the first 0x00 after kind|ws.
	for i := 9; i < len(key); i++ {
		if key[i] == 0x00 {
			return string(key[i+1:])
		}
	}
	return ""
}

// globalStats holds the vault-level BM25 statistics (stored under the FTS
// global-stats prefix).
type globalStats struct {
	N        uint64 // number of indexed documents
	TotalLen uint64 // sum of all docLens
}

func encodeStats(s globalStats) []byte {
	buf := make([]byte, 16)
	binary.LittleEndian.PutUint64(buf[0:8], s.N)
	binary.LittleEndian.PutUint64(buf[8:16], s.TotalLen)
	return buf
}

func decodeStats(buf []byte) globalStats {
	if len(buf) < 16 {
		return globalStats{}
	}
	return globalStats{
		N:        binary.LittleEndian.Uint64(buf[0:8]),
		TotalLen: binary.LittleEndian.Uint64(buf[8:16]),
	}
}

// readStats reads the global BM25 stats (N, TotalLen) from Pebble.
func (f *FTS) readStats(ws [8]byte) globalStats {
	val, closer, err := f.db.Get(keys.FTSGlobalStatsKey(ws))
	if err != nil || closer == nil {
		return globalStats{}
	}
	defer closer.Close()
	return decodeStats(val)
}

// writeStats writes the global BM25 stats (NoSync — called from a batch commit).
func (f *FTS) writeStats(ws [8]byte, b *pebble.Batch, s globalStats) {
	_ = b.Set(keys.FTSGlobalStatsKey(ws), encodeStats(s), nil)
}

// Index adds a document's fields to the Pebble-backed index. fields maps field
// names (mpn/description/tags/…) to their text. Re-indexing an existing id is a
// no-op (idempotency guard via the FTSIndexed key). The tf stored per posting is
// the field-weighted term frequency SUMMED across all fields. BM25 math is
// unchanged from go-rag.
func (f *FTS) Index(ws [8]byte, id string, fields map[string]string) {
	// Idempotency guard: if this id is already indexed, skip.
	idxKey := keys.FTSIndexedKey(ws, id)
	if val, closer, err := f.db.Get(idxKey); err == nil && closer != nil {
		closer.Close()
		_ = val // already indexed — no-op
		return
	}

	// Tokenize each field, accumulate weighted tf per term.
	termCounts := make(map[string]float64) // term → summed weighted tf
	docLen := 0
	for field, text := range fields {
		w := fieldWeight(field)
		for _, term := range Tokenize(text) {
			termCounts[term] += w
			docLen++
		}
	}
	if len(termCounts) == 0 {
		return // nothing to index (empty/all-stopword content)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Build one atomic batch: postings + indexed-set + stats.
	batch := f.db.NewBatch()
	for term, tf := range termCounts {
		_ = batch.Set(keys.FTSPostingKey(ws, term, id), encodePosting(float32(tf), docLen), nil)
	}
	_ = batch.Set(idxKey, encodePosting(0, docLen)[4:6], nil) // store docLen (2 bytes) for delete

	// Update global stats.
	s := f.readStats(ws)
	s.N++
	s.TotalLen += uint64(docLen)
	f.writeStats(ws, batch, s)

	// A corpus write changes N/df, so every cached IDF becomes potentially stale.
	f.idfCache = make(map[string]float64, 1024)

	_ = batch.Commit(pebble.NoSync)
}

// Delete removes a document from the index. content is the document's text
// (needed to recover the terms for key construction — the posting key shape puts
// id at the key's end, so an id-prefix scan is not possible). Idempotent
// (deleting absent keys is a Pebble no-op).
func (f *FTS) Delete(ws [8]byte, id, content string) {
	idxKey := keys.FTSIndexedKey(ws, id)

	// Read the stored docLen for stats update.
	val, closer, err := f.db.Get(idxKey)
	docLen := 0
	if err == nil && closer != nil {
		if len(val) >= 2 {
			docLen = int(binary.LittleEndian.Uint16(val[0:2]))
		}
		closer.Close()
	} else {
		return // not indexed — nothing to delete
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Collect unique terms from the content.
	termSet := make(map[string]struct{})
	for _, term := range Tokenize(content) {
		termSet[term] = struct{}{}
	}

	batch := f.db.NewBatch()
	for term := range termSet {
		_ = batch.Delete(keys.FTSPostingKey(ws, term, id), nil)
	}
	_ = batch.Delete(idxKey, nil)

	// Update global stats.
	s := f.readStats(ws)
	if s.N > 0 {
		s.N--
	}
	if s.TotalLen >= uint64(docLen) {
		s.TotalLen -= uint64(docLen)
	}
	f.writeStats(ws, batch, s)
	f.idfCache = make(map[string]float64, 1024)

	_ = batch.Commit(pebble.NoSync)
}

// Hit is a ranked search result.
type Hit struct {
	ID    string
	Score float64
}

// Search ranks documents by BM25 relevance to the query, returning the top k.
// The BM25 math is identical to go-rag's: k1=1.2, b=0.75, field-weighted tf,
// prefix expansion for short terms (<4 chars).
func (f *FTS) Search(ws [8]byte, query string, k int) []Hit {
	terms := Tokenize(query)
	if len(terms) == 0 {
		return nil
	}
	st := f.readStats(ws)
	n := float64(st.N)
	avgDL := 0.0
	if st.N > 0 {
		avgDL = float64(st.TotalLen) / float64(st.N)
	}

	scores := map[string]float64{}
	for _, term := range terms {
		// Scan postings for this term (exact match).
		posts := f.scanTerm(ws, term)
		if len(posts) == 0 && len(term) < 4 {
			// Prefix expansion: scan all terms starting with this prefix.
			posts = f.scanPrefix(ws, term)
		}
		if len(posts) == 0 {
			continue
		}
		df := float64(len(posts))
		idf := f.cachedIDF(ws, term, n, df)
		for id, tf := range posts {
			dl := f.docLenOf(ws, id)
			if dl == 0 {
				dl = avgDL
			}
			denom := tf + k1BM25*(1-bBM25+bBM25*dl/avgDL)
			scores[id] += idf * (tf * (k1BM25 + 1)) / denom
		}
	}

	hits := make([]Hit, 0, len(scores))
	for id, sc := range scores {
		hits = append(hits, Hit{ID: id, Score: sc})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if k > 0 && k < len(hits) {
		hits = hits[:k]
	}
	return hits
}

// scanTerm scans postings for an exact term and returns id → tf.
func (f *FTS) scanTerm(ws [8]byte, term string) map[string]float64 {
	lower := keys.FTSPostingTermPrefix(ws, term)
	return f.scanRange(lower, postingTermUpperBound(ws, term), term)
}

// scanPrefix scans postings for ALL terms starting with prefix (the prefix
// expansion for short queries). Returns id → summed tf (merged across terms).
func (f *FTS) scanPrefix(ws [8]byte, prefix string) map[string]float64 {
	if prefix == "" {
		return nil
	}
	lower := keys.FTSPostingTermPrefix(ws, prefix)
	upper := prefixRangeUpperBound(lower)
	if upper == nil {
		return nil // overflow (prefix ends with 0xFF) — skip
	}
	return f.scanRange(lower, upper, "")
}

// scanRange scans a Pebble key range, decoding postings and returning id → tf.
// knownTerm, when non-empty, allows direct id extraction (offset is fixed).
// When empty (prefix expansion), the id is extracted via the 0x00 separator.
func (f *FTS) scanRange(lower, upper []byte, knownTerm string) map[string]float64 {
	posts := map[string]float64{}
	iter, err := f.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return posts
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		var id string
		if knownTerm != "" {
			// Exact term: id starts after prefix(1) + ws(8) + term(n) + sep(1).
			off := 9 + len(knownTerm) + 1
			if len(key) > off {
				id = string(key[off:])
			}
		} else {
			id = idFromKey(key)
		}
		if id == "" {
			continue
		}
		tf, _ := decodePosting(iter.Value())
		posts[id] += float64(tf) // merge (prefix expansion may hit same id via different terms)
	}
	return posts
}

// docLenOf reads a document's docLen from its indexed-set entry.
func (f *FTS) docLenOf(ws [8]byte, id string) float64 {
	idxKey := keys.FTSIndexedKey(ws, id)
	val, closer, err := f.db.Get(idxKey)
	if err != nil || closer == nil {
		return 0
	}
	defer closer.Close()
	if len(val) >= 2 {
		return float64(binary.LittleEndian.Uint16(val[0:2]))
	}
	return 0
}

// Tokenize lowercases, splits on non-alphanumerics, and drops stopwords.
func Tokenize(s string) []string {
	s = strings.ToLower(s)
	out := make([]string, 0, 16)
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			w := b.String()
			if !isStopword(w) {
				out = append(out, w)
			}
			b.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func isStopword(w string) bool {
	switch w {
	case "the", "a", "an", "and", "or", "but", "of", "to", "in", "on", "for",
		"is", "are", "was", "were", "be", "been", "with", "as", "at", "by",
		"this", "that", "it", "from":
		return true
	}
	return false
}

// MigrateFromChunks builds the Pebble-backed FTS postings from existing records
// (one-time migration for a fresh FTS attached to an already-populated store).
// Verbatim from go-rag; the chunks callback yields (id, content) pairs from the
// caller's existing corpus. Uses a Pebble batch for efficiency.
func MigrateFromChunks(db *pebble.DB, ws [8]byte, chunks func(yield func(id, content string) bool)) error {
	batch := db.NewBatch()
	var n, totalLen uint64
	flush := func() {
		_ = batch.Commit(pebble.NoSync)
		batch = db.NewBatch()
	}
	count := 0
	chunks(func(id, content string) bool {
		terms := Tokenize(content)
		if len(terms) == 0 {
			return true // skip empty/all-stopword
		}
		termCounts := map[string]float64{}
		for _, t := range terms {
			termCounts[t] += weightBody
		}
		docLen := len(terms)
		for term, tf := range termCounts {
			_ = batch.Set(keys.FTSPostingKey(ws, term, id), encodePosting(float32(tf), docLen), nil)
		}
		idxKey := keys.FTSIndexedKey(ws, id)
		_ = batch.Set(idxKey, encodePosting(0, docLen)[4:6], nil)
		n++
		totalLen += uint64(docLen)
		count++
		if count >= 256 {
			flush()
		}
		return true // continue
	})
	_ = batch.Set(keys.FTSGlobalStatsKey(ws), encodeStats(globalStats{N: n, TotalLen: totalLen}), nil)
	_ = batch.Commit(pebble.NoSync)
	return nil
}

// HasPostings reports whether the Pebble-backed FTS has been initialized (the
// global stats key exists). Used to decide whether to run the one-time
// migration.
func HasPostings(db *pebble.DB, ws [8]byte) bool {
	val, closer, err := db.Get(keys.FTSGlobalStatsKey(ws))
	if err != nil || closer == nil {
		return false
	}
	closer.Close()
	return len(val) >= 16
}
