package procnet

import (
	"net/netip"
	"slices"
)

// Used by the Windows and macOS snapshots. An owned wildcard is normalized to
// loopback, but overlapping rows are rejected, including IPv6 wildcards whose
// IPv4 coverage cannot be verified. Rows with no known owner still compete.
type ownedBinding struct {
	addr netip.AddrPort
	pid  int
}

func selectOwnedListeners(rows []ownedBinding, owns func(int) bool, port uint16) []netip.AddrPort {
	byPort := make(map[uint16][]ownedBinding)
	for _, row := range rows {
		ip := row.addr.Addr().Unmap().WithZone("")
		if !ip.IsLoopback() && !ip.IsUnspecified() || row.addr.Port() == 0 || port != 0 && row.addr.Port() != port {
			continue
		}
		row.addr = netip.AddrPortFrom(ip, row.addr.Port())
		byPort[row.addr.Port()] = append(byPort[row.addr.Port()], row)
	}
	var result []netip.AddrPort
	for port, bindings := range byPort {
		for i, row := range bindings {
			if row.pid <= 0 || !owns(row.pid) {
				continue
			}
			ip := row.addr.Addr()
			if ip.IsUnspecified() {
				ip = netip.IPv6Loopback()
				if row.addr.Addr().Is4() {
					ip = netip.AddrFrom4([4]byte{127, 0, 0, 1})
				}
			}
			conflict := false
			for j, other := range bindings {
				if i == j {
					continue
				}
				binding := other.addr.Addr()
				if binding == ip || binding.IsUnspecified() && (binding.Is4() == ip.Is4() || binding.Is6()) {
					conflict = true
					break
				}
			}
			if !conflict {
				result = append(result, netip.AddrPortFrom(ip, port))
			}
		}
	}
	slices.SortFunc(result, netip.AddrPort.Compare)
	return slices.Compact(result)
}
