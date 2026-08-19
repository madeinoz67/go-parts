package tui

import (
	"strings"
	"testing"
)

func TestViewRendersRegions(t *testing.T) {
	m := testModel(nil)
	m.rows = []PartRow{{ID: "i1", MPN: "RC1", Description: "10k", Footprint: "0805", QtyOnHand: 45, ReorderPoint: 10}}
	m.cursor = 0
	m.hasDetail = true
	m.detail = PartDetail{Part: m.rows[0], Stock: []StockEntry{{Label: "Drawer A1", ViaCode: "L-Z", Quantity: 45}}}
	v := m.View()
	for _, cell := range []string{">RC1<", ">10k<", ">0805<", ">45<", ">Drawer A1<", "[a]djust", "[l]ow", "[q]uit"} {
		if !strings.Contains(v, cell) {
			t.Errorf("view missing %s:\n%s", cell, v)
		}
	}
}

func TestViewLowBadgeAndStatus(t *testing.T) {
	m := testModel(nil)
	m.rows = []PartRow{{MPN: "LOW1", QtyOnHand: 2, ReorderPoint: 10}}
	m.cursor = 0
	if v := m.View(); !strings.Contains(v, ">LOW<") {
		t.Errorf("low row must badge:\n%s", v)
	}
	m.status = "server unreachable"
	if v := m.View(); !strings.Contains(v, "server unreachable") {
		t.Errorf("status line must render:\n%s", v)
	}
}

func TestViewEmptyStates(t *testing.T) {
	m := testModel(nil) // zero rows
	if v := m.View(); !strings.Contains(v, "no parts") && !strings.Contains(v, "no matches") {
		t.Errorf("empty state must say which:\n%s", v)
	}
}

func TestViewAdjustOverlayCovers(t *testing.T) {
	m := testModel(nil)
	m.rows = []PartRow{{ID: "i1", MPN: "RC1"}}
	m.cursor = 0
	m.hasDetail = true
	m.detail = PartDetail{Part: m.rows[0], Stock: []StockEntry{{LocationID: "loc1", Label: "Bin", Quantity: 5}}}
	m.overlay = newAdjustModel(m.detail)
	if v := m.View(); !strings.Contains(v, "adjust") || !strings.Contains(v, "reason") {
		t.Errorf("overlay must render the form:\n%s", v)
	}
}
