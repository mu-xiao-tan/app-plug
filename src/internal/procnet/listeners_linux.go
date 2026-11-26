package procnet

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
)

type localSocketFD struct {
	path  string
	inode uint64
}

type localSocketOwner struct {
	pid   string
	start uint64
	fds   []localSocketFD
}

type localListenMatch struct {
	addr     netip.AddrPort
	conflict bool
}

type localListenTable struct {
	owned map[uint64]localListenMatch
	// 每个规范化绑定地址对应一个 inode；0 表示有多个或无法确认 inode。
	bindings map[netip.AddrPort]uint64
	port     uint16
}

func newLocalListenTable(owned map[uint64]localListenMatch, port uint16) localListenTable {
	return localListenTable{owned: owned, bindings: make(map[netip.AddrPort]uint64), port: port}
}

func (t localListenTable) add(inode uint64, addr netip.AddrPort) {
	if match, ok := t.owned[inode]; ok && !match.conflict {
		if match.addr.IsValid() && match.addr != addr {
			// 相同 inode 对应不同监听地址，不从冲突的快照推断归属。
			match.conflict = true
		} else {
			match.addr = addr
		}
		t.owned[inode] = match
	}
	if t.port != 0 && addr.Port() != t.port {
		return
	}
	ip := addr.Addr().Unmap()
	if !ip.IsLoopback() && !ip.IsUnspecified() {
		return
	}
	key := netip.AddrPortFrom(ip, addr.Port())
	if previous, ok := t.bindings[key]; ok && previous != inode {
		inode = 0
	}
	t.bindings[key] = inode
}

func (t localListenTable) destination(inode uint64) netip.AddrPort {
	match := t.owned[inode]
	if match.conflict || !match.addr.IsValid() || t.port != 0 && match.addr.Port() != t.port {
		return netip.AddrPort{}
	}
	ip := localLoopback(match.addr.Addr())
	if !ip.IsValid() {
		return netip.AddrPort{}
	}
	dst := netip.AddrPortFrom(ip, match.addr.Port())
	// procfs 未报告 IPV6_V6ONLY，因此 IPv6 通配也可能覆盖 IPv4 回环。
	if t.competes(dst, inode) ||
		t.competes(netip.AddrPortFrom(netip.IPv6Unspecified(), dst.Port()), inode) ||
		ip.Is4() && t.competes(netip.AddrPortFrom(netip.IPv4Unspecified(), dst.Port()), inode) {
		return netip.AddrPort{}
	}
	return dst
}

func (t localListenTable) competes(addr netip.AddrPort, inode uint64) bool {
	other, ok := t.bindings[addr]
	return ok && (other == 0 || other != inode)
}

// Listeners 只返回进程组持有、且在当前网络命名空间的监听表中没有其他监听竞争的回环 TCP 地址。
// port 为 0 时不限制端口；本函数不发起连接，读取结果不能保证监听状态不再变化。
func Listeners(pgid int, port uint16) ([]netip.AddrPort, error) {
	if pgid <= 0 {
		return nil, fmt.Errorf("无效的进程组 ID：%d", pgid)
	}
	pids, err := localProcNames("/proc")
	if err != nil {
		return nil, fmt.Errorf("读取进程列表：%w", err)
	}
	inodes := make(map[uint64]localListenMatch)
	var owners []localSocketOwner
	for _, pid := range pids {
		if _, err := strconv.ParseUint(pid, 10, 31); err != nil {
			continue
		}
		start, ok := localProcessStart(pid, pgid)
		if !ok {
			continue
		}
		dir := "/proc/" + pid + "/fd"
		names, err := localProcNames(dir)
		if err != nil {
			// 无法读取或已退出的进程不能提供属主证据。
			continue
		}
		owner := localSocketOwner{pid: pid, start: start}
		for _, name := range names {
			path := dir + "/" + name
			if inode := localSocketInode(path); inode != 0 {
				owner.fds = append(owner.fds, localSocketFD{path: path, inode: inode})
				inodes[inode] = localListenMatch{}
			}
		}
		if len(owner.fds) != 0 {
			owners = append(owners, owner)
		}
	}
	if len(inodes) == 0 {
		return nil, nil
	}
	table := newLocalListenTable(inodes, port)
	if err := readLocalListenTable("/proc/self/net/tcp", false, table); err != nil {
		return nil, err
	}
	if err := readLocalListenTable("/proc/self/net/tcp6", true, table); err != nil {
		return nil, err
	}

	// 在读取监听表前后核对同一描述符的 inode，并用启动时间拒绝 PID 复用。
	var listeners []netip.AddrPort
	for _, owner := range owners {
		n := len(listeners)
		for _, fd := range owner.fds {
			if dst := table.destination(fd.inode); dst.IsValid() && localSocketInode(fd.path) == fd.inode {
				listeners = append(listeners, dst)
			}
		}
		if len(listeners) != n {
			if start, ok := localProcessStart(owner.pid, pgid); !ok || start != owner.start {
				listeners = listeners[:n]
			}
		}
	}
	slices.SortFunc(listeners, netip.AddrPort.Compare)
	return slices.Compact(listeners), nil
}

func localProcNames(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Readdirnames(-1)
}

func localProcessStart(pid string, pgid int) (uint64, bool) {
	stat, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		return 0, false
	}
	// comm 可以包含空格、右括号和换行；从最后一个右括号开始解析后续字段。
	head := bytes.IndexByte(stat, '(')
	tail := bytes.LastIndexByte(stat, ')')
	if head != len(pid)+1 || tail <= head || tail+2 >= len(stat) || stat[len(pid)] != ' ' || stat[tail+1] != ' ' || string(stat[:len(pid)]) != pid {
		return 0, false
	}
	i := 0
	for field := range bytes.FieldsSeq(stat[tail+2:]) {
		switch i {
		case 0:
			if len(field) != 1 {
				return 0, false
			}
		case 2: // stat 第 5 列：pgrp。
			group, err := strconv.ParseUint(string(field), 10, 31)
			if err != nil || int(group) != pgid {
				return 0, false
			}
		case 19: // stat 第 22 列：starttime。
			start, err := strconv.ParseUint(string(field), 10, 64)
			return start, err == nil
		}
		i++
	}
	return 0, false
}

func localSocketInode(path string) uint64 {
	target, err := os.Readlink(path)
	if err != nil {
		return 0
	}
	value, ok := strings.CutPrefix(target, "socket:[")
	if !ok {
		return 0
	}
	value, ok = strings.CutSuffix(value, "]")
	if !ok {
		return 0
	}
	inode, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return inode
}

func readLocalListenTable(path string, ipv6 bool, table localListenTable) error {
	f, err := os.Open(path)
	if err != nil {
		// 未启用 IPv6 的内核可能没有 tcp6 表。
		if ipv6 && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("打开 TCP 监听表 %s：%w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var fields [10][]byte
		n := 0
		for field := range bytes.FieldsSeq(scanner.Bytes()) {
			fields[n] = field
			n++
			if n == len(fields) {
				break
			}
		}
		if n != len(fields) || !bytes.Equal(fields[3], []byte("0A")) {
			continue
		}
		inode, err := strconv.ParseUint(string(fields[9]), 10, 64)
		if err != nil {
			continue
		}
		addr := localListenAddress(&fields, ipv6)
		if addr.IsValid() {
			table.add(inode, addr)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 TCP 监听表 %s：%w", path, err)
	}
	return nil
}

func localLoopback(addr netip.Addr) netip.Addr {
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		if addr.Is4() {
			return netip.AddrFrom4([4]byte{127, 0, 0, 1})
		}
		return netip.IPv6Loopback()
	}
	if addr.IsLoopback() {
		return addr
	}
	return netip.Addr{}
}

func localListenAddress(fields *[10][]byte, ipv6 bool) netip.AddrPort {
	local := localProcEndpoint(fields[1], ipv6)
	if !local.IsValid() || local.Port() == 0 {
		return netip.AddrPort{}
	}
	// 校验 inode 之前的固定列，避免把缺列或内容错位的行当成监听记录。
	remote := localProcEndpoint(fields[2], ipv6)
	slot, ok := bytes.CutSuffix(fields[0], []byte(":"))
	if !ok || !localProcNumber(slot, 10, 32) || !remote.Addr().IsUnspecified() || remote.Port() != 0 ||
		!localProcHexPair(fields[4], 8) || !localProcHexPair(fields[5], 2) ||
		len(fields[6]) != 8 || !localProcNumber(fields[6], 16, 32) ||
		!localProcNumber(fields[7], 10, 32) || !localProcNumber(fields[8], 10, 32) {
		return netip.AddrPort{}
	}
	return local
}

func localProcEndpoint(field []byte, ipv6 bool) netip.AddrPort {
	var raw [16]byte
	n := 4
	if ipv6 {
		n = 16
	}
	if len(field) != n*2+5 || field[n*2] != ':' {
		return netip.AddrPort{}
	}
	port, err := strconv.ParseUint(string(field[n*2+1:]), 16, 16)
	if err != nil {
		return netip.AddrPort{}
	}
	if _, err := hex.Decode(raw[:n], field[:n*2]); err != nil {
		return netip.AddrPort{}
	}
	// 内核把地址按本机字节序的 32 位字打印为十六进制；IPv6 的四个字分别还原。
	for off := 0; off < n; off += 4 {
		word := raw[off : off+4]
		binary.NativeEndian.PutUint32(word, binary.BigEndian.Uint32(word))
	}
	if ipv6 {
		return netip.AddrPortFrom(netip.AddrFrom16(raw), uint16(port))
	}
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(raw[:4])), uint16(port))
}

func localProcHexPair(field []byte, left int) bool {
	return len(field) == left+1+8 && field[left] == ':' &&
		localProcNumber(field[:left], 16, 32) && localProcNumber(field[left+1:], 16, 32)
}

func localProcNumber(field []byte, base, bits int) bool {
	_, err := strconv.ParseUint(string(field), base, bits)
	return err == nil
}
