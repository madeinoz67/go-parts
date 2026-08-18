package rest

import "testing"

// TestIsLoopbackHost pins the loopback guard behind baseURL's label
// Host-header-poisoning defense (code-review finding 4): the whole 127.0.0.0/8
// block and IPv6 loopback (including IPv4-mapped) must pass — the old
// four-literal switch matched only 127.0.0.1, and its LastIndex(":")
// port-strip mangled portless IPv6 ("::1" became ":").
func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:7881", true},
		{"127.0.0.1", true},
		{"127.0.0.5:7881", true}, // 127.0.0.0/8 beyond .1 — missed by the literal switch
		{"127.200.3.9", true},    // far corner of the /8
		{"[::1]:7881", true},
		{"::1", true},              // portless IPv6 — mangled by the old port-strip
		{"::ffff:127.0.0.1", true}, // IPv4-mapped loopback
		{"localhost:7881", true},
		{"LOCALHOST", true},
		{"192.168.1.5:7881", false},
		{"10.0.0.1", false},
		{"example.com:80", false},
		{"[2001:db8::1]:80", false},
		{"::ffff:192.168.1.5", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}
