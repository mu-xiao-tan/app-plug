package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var (
	lockFileEx = kernel32.NewProc("LockFileEx")
	moveFileEx = kernel32.NewProc("MoveFileExW")
	convertSD  = syscall.NewLazyDLL("advapi32.dll").NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
)

func lockStateFile(f *os.File) error {
	const flags = 1 | 2 // LOCKFILE_FAIL_IMMEDIATELY | LOCKFILE_EXCLUSIVE_LOCK
	var overlapped syscall.Overlapped
	ok, _, err := lockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(f)
	if ok == 0 {
		return fmt.Errorf("LockFileEx: %w", err)
	}
	return nil // Windows releases the byte-range lock on closing the file handle.
}

func windowsPath(path string) (*uint16, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(abs, `\\?\`) && !strings.HasPrefix(abs, `\\.\`) {
		if unc, ok := strings.CutPrefix(abs, `\\`); ok {
			abs = `\\?\UNC\` + unc
		} else if len(abs) >= 240 {
			abs = `\\?\` + abs
		}
	}
	return syscall.UTF16PtrFromString(abs)
}

func openPrivateConfig(path string) (*os.File, error) {
	name, err := windowsPath(path)
	if err != nil {
		return nil, err
	}
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return nil, err
	}
	// Protected DACL: no inherited ACEs from a caller-selected directory.
	// Files are created private from the first write (including temp files).
	sddl, err := syscall.UTF16PtrFromString("D:P(A;;FA;;;" + sid + ")(A;;FA;;;SY)")
	if err != nil {
		return nil, err
	}
	var sd uintptr
	if ok, _, err := convertSD.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&sd)), 0); ok == 0 {
		return nil, fmt.Errorf("构建 Windows 私有文件 DACL：%w", err)
	}
	defer syscall.LocalFree(syscall.Handle(sd))
	attrs := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := syscall.CreateFile(name, syscall.GENERIC_WRITE, 0, &attrs, syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "创建私有文件", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func privateConfigTemp(dir string) (*os.File, error) {
	for range 16 {
		var suffix [16]byte
		_, err := rand.Read(suffix[:])
		if err != nil {
			return nil, err
		}
		f, err := openPrivateConfig(filepath.Join(dir, ".app-plug-"+hex.EncodeToString(suffix[:])))
		if !errors.Is(err, os.ErrExist) {
			return f, err
		}
	}
	return nil, os.ErrExist
}

func commitConfigFile(_, from, to string) error {
	src, err := windowsPath(from)
	if err != nil {
		return err
	}
	dst, err := windowsPath(to)
	if err != nil {
		return err
	}
	const flags = 1 | 8 // MOVEFILE_REPLACE_EXISTING | MOVEFILE_WRITE_THROUGH, no cross-volume copy.
	if ok, _, err := moveFileEx.Call(uintptr(unsafe.Pointer(src)), uintptr(unsafe.Pointer(dst)), flags); ok == 0 {
		return &os.LinkError{Op: "MoveFileExW", Old: from, New: to, Err: err}
	}
	return nil
}
