package procnet

import (
	"net/netip"
	"slices"
	"testing"
)

func TestOwnedListenerSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owned string
		other string
		want  string
	}{
		{"own wildcard", "0.0.0.0:8123", "", "127.0.0.1:8123"},
		{"unrelated loopback shadows wildcard", "0.0.0.0:8123", "127.0.0.1:8123", ""},
		{"unrelated IPv6 wildcard may shadow IPv4", "127.0.0.1:8123", "[::]:8123", ""},
		{"unrelated duplicate blocks routing", "127.0.0.1:8123", "127.0.0.1:8123", ""},
		{"other port is irrelevant", "[::1]:8123", "[::]:8124", "[::1]:8123"},
		{"public addresses are not routed", "192.0.2.1:8123", "", ""},
		{"configured port restricts discovery", "127.0.0.1:8124", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []ownedBinding{{netip.MustParseAddrPort(tc.owned), 42}}
			if tc.other != "" {
				rows = append(rows, ownedBinding{netip.MustParseAddrPort(tc.other), 99})
			}
			var want []netip.AddrPort
			if tc.want != "" {
				want = append(want, netip.MustParseAddrPort(tc.want))
			}
			got := selectOwnedListeners(rows, func(pid int) bool { return pid == 42 }, 8123)
			if !slices.Equal(got, want) {
				t.Fatalf("got %v; want %v", got, want)
			}
		})
	}
}
