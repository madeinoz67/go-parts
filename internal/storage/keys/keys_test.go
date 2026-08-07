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
