package rest

import (
	"net/http"
	"testing"
)

// TestDeletePart_RefusesWhenStocked pins the composed delete guard at the REST
// surface (finding 3): DELETE /parts/{id} on a part still stocked in a bin is
// 409 — deleting it would orphan the Component rows. The store-level guard is
// link.DeletePart; this proves the surface wires it and maps the sentinel.
func TestDeletePart_RefusesWhenStocked(t *testing.T) {
	srv := newTestServer(t)
	p := restCreate(t, srv, `{"MPN":"REST-STOCKED","PartType":"local"}`)
	if err := srv.components.Add("loc1", p.ID, 3, nil); err != nil {
		t.Fatal(err)
	}
	rr := deleteReq(srv, "/parts/"+p.ID)
	if rr.Code != http.StatusConflict {
		t.Fatalf("DELETE stocked part = %d, want 409 (body: %s)", rr.Code, rr.Body.String())
	}
	// Refused delete leaves the part.
	if rr := get(srv, "/parts/"+p.ID); rr.Code != http.StatusOK {
		t.Fatalf("GET after refused delete = %d, want 200", rr.Code)
	}
}

// TestDeletePart_UnstockedStill204: the guard must not break the ordinary
// delete of an unstocked part.
func TestDeletePart_UnstockedStill204(t *testing.T) {
	srv := newTestServer(t)
	p := restCreate(t, srv, `{"MPN":"REST-UNSTOCKED","PartType":"local"}`)
	if rr := deleteReq(srv, "/parts/"+p.ID); rr.Code != http.StatusNoContent {
		t.Fatalf("DELETE unstocked part = %d, want 204", rr.Code)
	}
}
