package components

import (
	"encoding/json"
	"testing"
	"time"
)

// TestComponentJSONRoundTrip verifies the v1 storage contract: a fully-populated
// Component round-trips through encoding/json with every field preserved, and a
// zero-value Component round-trips without panicking. The store serializes the
// struct as JSON under the components keyspace (0x13); any field the round-trip
// drops would be silently destroyed on the first writeback.
func TestComponentJSONRoundTrip(t *testing.T) {
	// JSON time resolution is microsecond (RFC3339Nano truncates); truncate so
	// the equality check below doesn't flap on sub-µs precision loss.
	now := time.Now().UTC().Truncate(time.Microsecond)
	c := Component{
		LocationID: "01J000000000000000000000AB",
		PartID:     "01J000000000000000000000CD",
		Quantity:   42,
		Tags:       []string{"preferred", "reel"},
		History: []Movement{
			{Timestamp: now, Delta: 100, Reason: "initial order"},
			{Timestamp: now, Delta: -58, Reason: "used in project X"},
		},
		CreatedAt: now,
		UpdatedAt: now,
		Version:   3,
	}

	data, err := json.Marshal(&c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Component
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.LocationID != c.LocationID {
		t.Errorf("LocationID: got %q want %q", got.LocationID, c.LocationID)
	}
	if got.PartID != c.PartID {
		t.Errorf("PartID: got %q want %q", got.PartID, c.PartID)
	}
	if got.Quantity != c.Quantity {
		t.Errorf("Quantity: got %d want %d", got.Quantity, c.Quantity)
	}
	if len(got.Tags) != len(c.Tags) {
		t.Errorf("Tags len: got %d want %d", len(got.Tags), len(c.Tags))
	} else {
		for i := range c.Tags {
			if got.Tags[i] != c.Tags[i] {
				t.Errorf("Tags[%d]: got %q want %q", i, got.Tags[i], c.Tags[i])
			}
		}
	}
	if len(got.History) != len(c.History) {
		t.Fatalf("History len: got %d want %d", len(got.History), len(c.History))
	}
	for i := range c.History {
		if !got.History[i].Timestamp.Equal(c.History[i].Timestamp) {
			t.Errorf("History[%d].Timestamp: got %v want %v", i, got.History[i].Timestamp, c.History[i].Timestamp)
		}
		if got.History[i].Delta != c.History[i].Delta {
			t.Errorf("History[%d].Delta: got %d want %d", i, got.History[i].Delta, c.History[i].Delta)
		}
		if got.History[i].Reason != c.History[i].Reason {
			t.Errorf("History[%d].Reason: got %q want %q", i, got.History[i].Reason, c.History[i].Reason)
		}
	}
	if !got.CreatedAt.Equal(c.CreatedAt) {
		t.Errorf("CreatedAt: got %v want %v", got.CreatedAt, c.CreatedAt)
	}
	if !got.UpdatedAt.Equal(c.UpdatedAt) {
		t.Errorf("UpdatedAt: got %v want %v", got.UpdatedAt, c.UpdatedAt)
	}
	if got.Version != c.Version {
		t.Errorf("Version: got %d want %d", got.Version, c.Version)
	}
}

// TestComponentZeroRoundTrip verifies a zero-value Component (no History, no
// Tags, zero timestamps) round-trips without panic and without fabricating
// fields — important because the store will eventually unmarshal into a freshly
// zeroed Component on every Get.
func TestComponentZeroRoundTrip(t *testing.T) {
	var c Component
	data, err := json.Marshal(&c)
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	var got Component
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal zero: %v", err)
	}
	if got.Quantity != 0 || got.Version != 0 || got.LocationID != "" || got.PartID != "" {
		t.Errorf("zero round-trip fabricated fields: %+v", got)
	}
	if len(got.History) != 0 || len(got.Tags) != 0 {
		t.Errorf("zero round-trip fabricated slices: History=%v Tags=%v", got.History, got.Tags)
	}
}

// TestMovementJSONRoundTrip verifies a Movement decodes cleanly — the store
// appends Movements to Component.History on every stock change, so a decode
// regression would silently drop audit-trail entries.
func TestMovementJSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	m := Movement{Timestamp: now, Delta: -7, Reason: "transferred to Bin A2"}
	data, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Movement
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got.Timestamp.Equal(m.Timestamp) {
		t.Errorf("Timestamp: got %v want %v", got.Timestamp, m.Timestamp)
	}
	if got.Delta != m.Delta {
		t.Errorf("Delta: got %d want %d", got.Delta, m.Delta)
	}
	if got.Reason != m.Reason {
		t.Errorf("Reason: got %q want %q", got.Reason, m.Reason)
	}
}
