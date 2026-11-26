package procnet

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"
)

var tcpTableAPI = sync.OnceValues(func() (*syscall.Proc, error) {
	// Resolve the system copy, not a DLL from the application working directory.
	var dir [syscall.MAX_PATH]uint16
	getDir := syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")
	n, _, err := getDir.Call(uintptr(unsafe.Pointer(&dir[0])), uintptr(len(dir)))
	if n == 0 || n >= uintptr(len(dir)) {
		return nil, fmt.Errorf("GetSystemDirectoryW: %w", err)
	}
	dll, loadErr := syscall.LoadDLL(filepath.Join(syscall.UTF16ToString(dir[:n]), "iphlpapi.dll"))
	if loadErr != nil {
		return nil, loadErr
	}
	proc, loadErr := dll.FindProc("GetExtendedTcpTable")
	if loadErr != nil {
		dll.Release()
	}
	return proc, loadErr
})

// Listeners matches TCP_TABLE_OWNER_PID_LISTENER rows against processes the
// caller has independently verified and kept alive/identified by handle.
func Listeners(owns func(int) bool, port uint16) ([]netip.AddrPort, error) {
	var rows []ownedBinding
	api, err := tcpTableAPI()
	if err != nil {
		return nil, err
	}
	for _, ipv6 := range []bool{false, true} {
		family := uintptr(syscall.AF_INET)
		if ipv6 {
			family = syscall.AF_INET6
		}
		var size uint32
		var data []byte
		var complete bool
		for range 5 {
			var ptr *byte
			if len(data) > 0 {
				ptr = &data[0]
			}
			const ownerPIDListener = 3
			code, _, _ := api.Call(uintptr(unsafe.Pointer(ptr)), uintptr(unsafe.Pointer(&size)), 0, family, ownerPIDListener, 0)
			if code == 0 {
				if uint64(size) > uint64(len(data)) {
					return nil, fmt.Errorf("Windows 返回了不完整的 TCP 监听表")
				}
				found, err := parseWindowsTCP(data[:size], ipv6)
				if err != nil {
					return nil, err
				}
				rows = append(rows, found...)
				complete = true
				break
			}
			if code != 122 || size <= uint32(len(data)) || size > 32<<20 { // ERROR_INSUFFICIENT_BUFFER
				return nil, fmt.Errorf("GetExtendedTcpTable(AF=%d): %w", family, syscall.Errno(code))
			}
			data = make([]byte, size)
		}
		if !complete {
			return nil, fmt.Errorf("Windows TCP 监听表在读取期间反复变化")
		}
	}
	return selectOwnedListeners(rows, owns, port), nil
}
