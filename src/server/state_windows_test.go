package server

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func TestWindowsPrivateConfigDACLBeforeAndAfterReplace(t *testing.T) {
	dir, err := os.MkdirTemp(t.TempDir(), "config with spaces-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admin-token")
	if err := createPrivateConfigFile(path, []byte("synthetic fixture\n")); err != nil {
		t.Fatal(err)
	}
	assertPrivateWindowsDACL(t, path)
	if err := writeJSONAtomic(path, "replacement", 1024); err != nil {
		t.Fatal(err)
	}
	assertPrivateWindowsDACL(t, path)
}

func assertPrivateWindowsDACL(t *testing.T, path string) {
	t.Helper()
	name, err := windowsPath(path)
	if err != nil {
		t.Fatal(err)
	}
	const daclSecurityInformation = 4
	advapi := syscall.NewLazyDLL("advapi32.dll")
	getSecurity := advapi.NewProc("GetFileSecurityW")
	asString := advapi.NewProc("ConvertSecurityDescriptorToStringSecurityDescriptorW")
	var size uint32
	getSecurity.Call(uintptr(unsafe.Pointer(name)), daclSecurityInformation, 0, 0, uintptr(unsafe.Pointer(&size)))
	if size == 0 || size > 1<<16 {
		t.Fatalf("invalid security descriptor size: %d", size)
	}
	sd := make([]byte, size)
	if ok, _, err := getSecurity.Call(uintptr(unsafe.Pointer(name)), daclSecurityInformation, uintptr(unsafe.Pointer(&sd[0])), uintptr(size), uintptr(unsafe.Pointer(&size))); ok == 0 {
		t.Fatalf("GetFileSecurityW: %v", err)
	}
	var text *uint16
	var length uint32
	if ok, _, err := asString.Call(uintptr(unsafe.Pointer(&sd[0])), 1, daclSecurityInformation, uintptr(unsafe.Pointer(&text)), uintptr(unsafe.Pointer(&length))); ok == 0 {
		t.Fatalf("ConvertSecurityDescriptorToStringSecurityDescriptorW: %v", err)
	}
	defer syscall.LocalFree(syscall.Handle(uintptr(unsafe.Pointer(text))))
	if text == nil || length == 0 || length > 1<<16 {
		t.Fatal("invalid security descriptor string")
	}
	sddl := syscall.UTF16ToString(unsafe.Slice(text, int(length)))
	if !strings.HasPrefix(sddl, "D:P") || strings.Count(sddl, "(") != 2 || !strings.Contains(sddl, ";;;SY)") {
		t.Fatal("private file does not have a protected two-entry DACL including LocalSystem")
	}
}
