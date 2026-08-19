// Package tui is the terminal surface (PRD §5.11): a Bubble Tea REST client of
// a running go-parts daemon — never a second Pebble opener. client.go is the
// typed wire layer: plain net/http, zero Bubble Tea imports, so it tests
// against the real rest.Server via httptest with no terminal machinery.
package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// PartRow is the slim wire view of a part the table needs. The server sends
// the full PascalCase Part JSON; decoding into this subset is lossless for the
// named fields and keeps the client decoupled from internal packages.
type PartRow struct {
	ID           string
	MPN          string
	Description  string
	Footprint    string
	QtyOnHand    int
	ReorderPoint int
}

// StatsResult mirrors GET /stats.
type StatsResult struct {
	PartsTotal int `json:"parts_total"`
}

// Client is the typed REST client. base has no trailing slash; every call has
// a 5s timeout — the TUI degrades loudly, never hangs.
type Client struct {
	base string
	hc   *http.Client
}

// NewClient wires a Client against base (e.g. "http://127.0.0.1:7890").
func NewClient(base string) *Client {
	return &Client{base: base, hc: &http.Client{Timeout: 5 * time.Second}}
}

// Stats fetches GET /stats.
func (c *Client) Stats() (StatsResult, error) {
	var out StatsResult
	err := c.getJSON("/stats", &out)
	return out, err
}

// Search fetches GET /parts?q=… (BM25 top-20 — the documented REST depth).
func (c *Client) Search(q string) ([]PartRow, error) {
	return c.rows("/parts?q=" + url.QueryEscape(q))
}

// BrowseAll fetches GET /parts?all=1 — the corpus, store.List order.
func (c *Client) BrowseAll() ([]PartRow, error) { return c.rows("/parts?all=1") }

// LowStock fetches GET /parts?all=1&low=1 (QtyOnHand <= ReorderPoint, 0/0 low).
func (c *Client) LowStock() ([]PartRow, error) { return c.rows("/parts?all=1&low=1") }

func (c *Client) rows(path string) ([]PartRow, error) {
	var out []PartRow
	if err := c.getJSON(path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// getJSON does the one GET + decode every method shares. A non-200 returns
// the response body verbatim as the error — the engine's own sentinel text,
// never a TUI-invented message.
func (c *Client) getJSON(path string, out any) error {
	resp, err := c.hc.Get(c.base + path)
	if err != nil {
		return fmt.Errorf("go-parts server unreachable at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var body []byte
		body, _ = io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// StockEntry is one bin's stock of a part. LocationID (Task 8) is the bin's
// ULID — the stock-adjust overlay PATCHes /locations/{id}/components/{partId},
// which addresses the write by id, not by label/via-code.
type StockEntry struct {
	LocationID string
	Label      string
	ViaCode    string
	Quantity   int
}

// Movement is one stock movement, decoded from the PascalCase wire.
type Movement struct {
	Timestamp time.Time
	Delta     int
	Reason    string
}

// PartDetail is the GET /parts/{id}/stock aggregate the detail pane renders.
type PartDetail struct {
	Part            PartRow
	Stock           []StockEntry
	RecentMovements []Movement
}

// GetPart fetches GET /parts/{id}/stock — full detail for the bottom pane.
func (c *Client) GetPart(id string) (PartDetail, error) {
	var out PartDetail
	if err := c.getJSON("/parts/"+url.PathEscape(id)+"/stock", &out); err != nil {
		return PartDetail{}, err
	}
	return out, nil
}

// Adjust PATCHes the component at (locID, partID) with a signed delta and the
// required reason (the movement-history audit trail). A non-200 returns the
// response body verbatim (e.g. "404 page not found" on an un-stocked pair)
// so the adjust form can show it and the user can fix and retry.
func (c *Client) Adjust(locID, partID string, delta int, reason string) error {
	body, _ := json.Marshal(map[string]any{"Delta": delta, "Reason": reason})
	req, err := http.NewRequest(http.MethodPatch,
		c.base+"/locations/"+url.PathEscape(locID)+"/components/"+url.PathEscape(partID),
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("go-parts server unreachable at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b []byte
		b, _ = io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
