package procnet

import (
	"encoding/binary"
	"net/netip"
	"slices"
	"testing"
)

func TestLsofFieldSets(t *testing.T) {
	const data = "p42\x00g12\x00\nf4\x00tIPv4\x00n*:8123\x00\nf5\x00tIPv6\x00n[::1]:8124\x00\np99\x00g99\x00\nf8\x00tIPv4\x00n127.0.0.1:8123\x00\n"
	rows, groups, err := parseLsofListeners([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.AddrPort{netip.MustParseAddrPort("[::1]:8124")}
	if got := selectOwnedListeners(rows, func(pid int) bool { return groups[pid] == 12 }, 0); !slices.Equal(got, want) {
		t.Fatalf("got %v; want %v", got, want)
	}
	if _, _, err := parseLsofListeners([]byte("p42\x00g12\x00\nf4\x00n*:8123\x00")); err == nil {
		t.Fatal("accepted wildcard with no address family")
	}
}

func TestWindowsTCPTables(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		width, addr, port, state, pid := 24, 4, 8, 0, 20
		ip := netip.MustParseAddr("127.0.0.1")
		if ipv6 {
			width, addr, port, state, pid = 56, 0, 20, 48, 52
			ip = netip.IPv6Loopback()
		}
		data := make([]byte, 4+2*width)
		binary.LittleEndian.PutUint32(data, 2)
		for i := range 2 {
			row := data[4+i*width:][:width]
			copy(row[addr:], ip.AsSlice())
			binary.BigEndian.PutUint16(row[port:], 8123)
			binary.LittleEndian.PutUint32(row[state:], uint32(2+i))
			binary.LittleEndian.PutUint32(row[pid:], uint32(42+i))
		}
		got, err := parseWindowsTCP(data, ipv6)
		want := []ownedBinding{{netip.AddrPortFrom(ip, 8123), 42}}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("IPv6=%t: got %v, %v; want %v", ipv6, got, err, want)
		}
		if _, err := parseWindowsTCP(data[:len(data)-1], ipv6); err == nil {
			t.Fatal("accepted truncated TCP table")
		}
	}
}
