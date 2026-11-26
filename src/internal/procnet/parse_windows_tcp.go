package procnet

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

func parseWindowsTCP(data []byte, ipv6 bool) ([]ownedBinding, error) {
	const listen = 2 // MIB_TCP_STATE_LISTEN
	width, addrOff, portOff, stateOff, pidOff := 24, 4, 8, 0, 20
	if ipv6 {
		width, addrOff, portOff, stateOff, pidOff = 56, 0, 20, 48, 52
	}
	if len(data) < 4 || uint64(binary.LittleEndian.Uint32(data)) > uint64((len(data)-4)/width) {
		return nil, fmt.Errorf("Windows TCP 监听表长度不完整")
	}
	count := int(binary.LittleEndian.Uint32(data))
	rows := make([]ownedBinding, 0, count)
	for i := range count {
		row := data[4+i*width:][:width]
		if binary.LittleEndian.Uint32(row[stateOff:]) != listen {
			continue
		}
		ip := netip.Addr{}
		if ipv6 {
			ip = netip.AddrFrom16([16]byte(row[addrOff : addrOff+16]))
		} else {
			ip = netip.AddrFrom4([4]byte(row[addrOff : addrOff+4]))
		}
		rows = append(rows, ownedBinding{addr: netip.AddrPortFrom(ip, binary.BigEndian.Uint16(row[portOff:])), pid: int(binary.LittleEndian.Uint32(row[pidOff:]))})
	}
	return rows, nil
}
