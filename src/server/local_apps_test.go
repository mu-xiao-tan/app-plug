package server

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMissingLocalExecutableDoesNotInvalidateConfiguration(t *testing.T) {
	s, p := newLocalTestStore(t)
	file := filepath.Join(s.dir, "old-release")
	p.Launchers = map[string]string{"legacy": file}
	p.LocalApps = []localAppConfig{{Domain: "file", StartFile: file}}
	if err := writeJSONAtomic(s.configPath, p, maxPolicyBytes); err != nil {
		t.Fatal(err)
	}
	routes := []route{
		{Owner: "admin", Domain: "persisted", StartFile: file, AutoStart: true},
		{Owner: "admin", Domain: "legacy", Launcher: "legacy"},
		{Owner: "admin", Domain: "existing", Address: "127.0.0.1:8080", routeInput: routeInput{Name: "Existing"}},
	}
	if err := writeJSONAtomic(filepath.Join(s.dir, "routes.json"), routes, maxStateBytes); err != nil {
		t.Fatal(err)
	}
	if count, _, err := CheckConfig(s.configPath, s.dir, ""); err != nil || count != 4 {
		t.Fatalf("configuration check: got %d routes, err %v; want all 4 routes", count, err)
	}
	p, err := loadPolicy(s.configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadRoutes(s.dir, p)
	if err != nil {
		t.Fatal(err)
	}
	s.publish(c)
	for _, domain := range []string{"file", "persisted", "legacy"} {
		a := s.current.Load().byHost[domain+".example.test"]
		if a == nil {
			t.Fatalf("%s should remain registered", domain)
		}
		state := a.response()
		if state.Running == nil || *state.Running || state.Ready == nil || *state.Ready {
			t.Fatalf("%s should remain stopped and unready", domain)
		}
	}
	if a := s.current.Load().byHost["existing.example.test"]; a == nil || a.upstream() == nil || a.upstream().Host != "127.0.0.1:8080" {
		t.Fatalf("existing route should remain available: %v", a)
	}
	if _, err := p.resolveApp(appInput{Domain: "persisted", StartFile: file}, "admin"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upserting a missing executable: got %v; want a missing-file error", err)
	}
	if err := s.reload(); err != nil {
		t.Fatalf("reloading with a missing executable: %v", err)
	}
}

func TestLocalReloadSwapsPorts(t *testing.T) {
	s, p := newLocalTestStore(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	first, second := localTestPorts(t)
	config := func(domain string, port uint16) localAppConfig {
		return localAppConfig{Domain: domain, StartFile: exe, Port: port,
			Args: []string{"-test.run=^TestLocalListenerProcess$", "--", strconv.Itoa(int(port))}}
	}
	p.LocalApps = []localAppConfig{config("a", first), config("b", second)}
	c, err := p.catalog(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.publish(c)
	waitForLocalPorts(t, s, first, second)

	p.LocalApps = []localAppConfig{config("a", second), config("b", first)}
	if err := writeJSONAtomic(s.configPath, p, maxPolicyBytes); err != nil {
		t.Fatal(err)
	}
	if err := s.reload(); err != nil {
		t.Fatal(err)
	}
	waitForLocalPorts(t, s, second, first)
}

func TestLocalListenerProcess(t *testing.T) {
	if len(os.Args) != 4 || os.Args[1] != "-test.run=^TestLocalListenerProcess$" || os.Args[2] != "--" {
		return
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+os.Args[3])
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func newLocalTestStore(t *testing.T) (*routeStore, *policy) {
	t.Helper()
	p := defaultPolicy()
	p.BaseDomain, p.HomeHost = "example.test", "nav.example.test"
	p.Principals = map[string]principal{"admin": {Admin: true, TokenSHA256: strings.Repeat("0", 64)}}
	if err := p.validate(""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := &routeStore{dir: dir, configPath: filepath.Join(dir, "config.json"), changed: make(chan struct{}, 1)}
	t.Cleanup(s.locals.close)
	return s, &p
}

func localTestPorts(t *testing.T) (uint16, uint16) {
	t.Helper()
	var ports [2]uint16
	for i := range ports {
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		ports[i] = uint16(listener.Addr().(*net.TCPAddr).Port)
	}
	return ports[0], ports[1]
}

func waitForLocalPorts(t *testing.T, s *routeStore, first, second uint16) {
	t.Helper()
	c := s.current.Load()
	a, b := c.byHost["a.example.test"], c.byHost["b.example.test"]
	wantA, wantB := strconv.Itoa(int(first)), strconv.Itoa(int(second))
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		upstreamA, upstreamB := a.upstream(), b.upstream()
		if upstreamA != nil && upstreamB != nil && upstreamA.Port() == wantA && upstreamB.Port() == wantB {
			return
		}
		select {
		case <-s.changed:
		case <-deadline.C:
			t.Fatalf("managed listeners: got %v, %v; want ports %s, %s", a.upstream(), b.upstream(), wantA, wantB)
		}
	}
}
