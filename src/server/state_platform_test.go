package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateLockContentionAndRelease(t *testing.T) {
	dir := t.TempDir()
	f, err := lockState(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if second, err := lockState(dir); err == nil {
		second.Close()
		t.Fatal("same state directory acquired twice")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	later, err := lockState(dir)
	if err != nil {
		t.Fatalf("close did not release state lock: %v", err)
	}
	defer later.Close()
}

func TestConfigStateReplacementWithSpacedPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config with spaces")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := createPrivateConfigFile(path, []byte("null\n")); err != nil {
		t.Fatal(err)
	}
	if err := createPrivateConfigFile(path, []byte("overwrite\n")); err == nil {
		t.Fatal("private config creation overwrote an existing file")
	}
	if err := writeJSONAtomic(path, []int{42}, 1024); err != nil {
		t.Fatal(err)
	}
	var got []int
	if err := readJSON(path, 1024, &got); err != nil || len(got) != 1 || got[0] != 42 {
		t.Fatalf("replacement not readable: %v, %v", got, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("temporary config files left behind: %v, %v", entries, err)
	}
}
