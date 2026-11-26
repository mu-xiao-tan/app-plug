package procnet

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalListenDestination(t *testing.T) {
	const ipv4 = "127.0.0.1:8080"
	const ipv6 = "[::1]:8080"
	for _, tt := range []struct {
		port       uint16
		name       string
		owned      string
		other      string
		otherInode uint64
		otherOwned bool
		want       string
	}{
		{name: "uncontested", owned: ipv4, want: ipv4},
		{name: "matching configured port", port: 8080, owned: ipv4, want: ipv4},
		{name: "different configured port", port: 8081, owned: ipv4},
		{name: "duplicate record for same socket", owned: ipv4, other: ipv4, otherInode: 1, want: ipv4},
		{name: "same inode reports different endpoints", owned: ipv4, other: "127.0.0.2:8080", otherInode: 1},
		{name: "another socket on same endpoint", owned: ipv4, other: ipv4, otherInode: 2},
		{name: "another owned socket on same endpoint", owned: ipv4, other: ipv4, otherInode: 2, otherOwned: true},
		{name: "socket with no reported inode", owned: ipv4, other: ipv4},
		{name: "same port on another loopback address", owned: ipv4, other: "127.0.0.2:8080", otherInode: 2, want: ipv4},
		{name: "same port on nonloopback address", owned: ipv4, other: "192.0.2.1:8080", otherInode: 2, want: ipv4},
		{name: "same address on another port", owned: ipv4, other: "127.0.0.1:8081", otherInode: 2, want: ipv4},
		{name: "IPv4 wildcard covers exact IPv4", owned: ipv4, other: "0.0.0.0:8080", otherInode: 2},
		{name: "exact IPv4 competes with owned wildcard", owned: "0.0.0.0:8080", other: ipv4, otherInode: 2},
		{name: "owned wildcard publishes only default loopback", owned: "0.0.0.0:8080", other: "127.0.0.2:8080", otherInode: 2, want: ipv4},
		{name: "IPv6 wildcard may also cover IPv4", owned: ipv4, other: "[::]:8080", otherInode: 2},
		{name: "mapped IPv4 competes with native IPv4", owned: "[::ffff:127.0.0.1]:8080", other: ipv4, otherInode: 2},
		{name: "mapped IPv4 wildcard covers exact IPv4", owned: ipv4, other: "[::ffff:0.0.0.0]:8080", otherInode: 2},
		{name: "IPv6 wildcard covers exact IPv6", owned: ipv6, other: "[::]:8080", otherInode: 2},
		{name: "exact IPv6 competes with owned wildcard", owned: "[::]:8080", other: ipv6, otherInode: 2},
		{name: "IPv4 wildcard does not cover IPv6", owned: ipv6, other: "0.0.0.0:8080", otherInode: 2, want: ipv6},
		{name: "IPv6 wildcard publishes IPv6 only", owned: "[::]:8080", other: ipv4, otherInode: 2, want: ipv6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			owned := map[uint64]localListenMatch{1: {}}
			if tt.otherOwned {
				owned[tt.otherInode] = localListenMatch{}
			}
			table := newLocalListenTable(owned, tt.port)
			table.add(1, netip.MustParseAddrPort(tt.owned))
			if tt.other != "" {
				table.add(tt.otherInode, netip.MustParseAddrPort(tt.other))
			}
			var want netip.AddrPort
			if tt.want != "" {
				want = netip.MustParseAddrPort(tt.want)
			}
			if got := table.destination(1); got != want {
				t.Fatalf("destination() = %v, want %v", got, want)
			}
			if tt.otherOwned {
				if got := table.destination(tt.otherInode); got.IsValid() {
					t.Fatalf("destination(%d) = %v, want ambiguous listener omitted", tt.otherInode, got)
				}
			}
		})
	}
}

func TestReadLocalListenTableCountsUnownedListeners(t *testing.T) {
	dir := t.TempDir()
	tcp := filepath.Join(dir, "tcp")
	tcp6 := filepath.Join(dir, "tcp6")
	const header = "  sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode\n"
	const rows = header +
		"   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 1\n" +
		"   1: 00000000:1F91 00000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 4\n" +
		"   2: 00000000:1F91 00000000:0000 01 00000000:00000000 00:00000000 00000000 1000 0 3\n"
	// A wildcard LISTEN without target FD evidence must still count across address families.
	const rows6 = header +
		"   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000 1000 0 2\n"
	if err := os.WriteFile(tcp, []byte(rows), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tcp6, []byte(rows6), 0600); err != nil {
		t.Fatal(err)
	}
	table := newLocalListenTable(map[uint64]localListenMatch{1: {}, 4: {}}, 0)
	if err := readLocalListenTable(tcp, false, table); err != nil {
		t.Fatal(err)
	}
	if err := readLocalListenTable(tcp6, true, table); err != nil {
		t.Fatal(err)
	}
	if got := table.destination(1); got.IsValid() {
		t.Errorf("destination() = %v, want ambiguous listener omitted", got)
	}
	uncontested := netip.MustParseAddrPort("127.0.0.1:8081")
	if got := table.destination(4); got != uncontested {
		t.Errorf("destination() = %v, want %v (non-LISTEN rows do not count)", got, uncontested)
	}
}
