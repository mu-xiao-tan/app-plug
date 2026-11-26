package procnet

import (
	"bytes"
	"fmt"
	"net/netip"
	"strconv"
)

func parseLsofListeners(data []byte) ([]ownedBinding, map[int]int, error) {
	var rows []ownedBinding
	groups := make(map[int]int)
	var pid int
	var family string
	for raw := range bytes.SplitSeq(data, []byte{0}) {
		field := bytes.TrimLeft(raw, "\n") // lsof terminates each set with an extra newline.
		if len(field) == 0 {
			continue
		}
		value := string(field[1:])
		switch field[0] {
		case 'p':
			pid, _ = strconv.Atoi(value)
			family = ""
		case 'g':
			groups[pid], _ = strconv.Atoi(value)
		case 'f':
			family = ""
		case 't':
			family = value
		case 'n':
			if family != "IPv4" && family != "IPv6" {
				return nil, nil, fmt.Errorf("lsof 未报告 TCP 监听的地址族")
			}
			if len(value) >= 2 && value[:2] == "*:" {
				if family == "IPv4" {
					value = "0.0.0.0" + value[1:]
				} else {
					value = "[::]" + value[1:]
				}
			}
			addr, err := netip.ParseAddrPort(value)
			if err != nil || addr.Port() == 0 || pid <= 0 {
				return nil, nil, fmt.Errorf("无法解析 lsof 的 TCP 监听记录")
			}
			rows = append(rows, ownedBinding{addr: addr, pid: pid})
		default:
			return nil, nil, fmt.Errorf("无法解析 lsof 监听字段")
		}
	}
	return rows, groups, nil
}
