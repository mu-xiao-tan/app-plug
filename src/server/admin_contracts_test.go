package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	contractAdminToken = "test-console-admin-token"
	contractUserToken  = "test-console-user-token"
	contractDomain     = "demo.example.test"
)

func contractTestPolicy() policy {
	p := defaultPolicy()
	p.BaseDomain, p.HomeHost = "example.test", "nav.example.test"
	p.Principals = map[string]principal{
		"admin": {Admin: true, TokenSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(contractAdminToken)))},
		"user":  {Admin: true, TokenSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(contractUserToken)))},
	}
	return p
}

func contractTestServer(t *testing.T, p policy, routes []route) (*routeStore, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	s := &routeStore{dir: dir, configPath: filepath.Join(dir, "config.json"), changed: make(chan struct{}, 1)}
	if err := writeJSONAtomic(s.configPath, &p, maxPolicyBytes); err != nil {
		t.Fatal(err)
	}
	if len(routes) > 0 {
		if err := writeJSONAtomic(filepath.Join(dir, "routes.json"), routes, maxStateBytes); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := loadPolicy(s.configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := loadRoutes(dir, loaded)
	if err != nil {
		t.Fatal(err)
	}
	s.publish(c)
	t.Cleanup(s.locals.close)
	return s, newNavigator(s)
}

func contractRequest(t *testing.T, handler http.Handler, token, method, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1"
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != wantStatus {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, w.Code, wantStatus, w.Body.String())
	}
	return w
}

func contractIssueKey(t *testing.T, handler http.Handler, issuer, body string) string {
	t.Helper()
	w := contractRequest(t, handler, issuer, http.MethodPost, "/admin/deploy-keys", body, http.StatusCreated)
	var receipt deployKeyReceipt
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(receipt.Token, deployKeyPrefix) {
		t.Fatal("expected a temporary deployment key")
	}
	return receipt.Token
}

func contractLegacyApp(t *testing.T) (policy, route, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "start.exe")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p := contractTestPolicy()
	p.Launchers = map[string]string{"demo": file}
	grant := p.Principals["user"]
	grant.Admin = false
	grant.Domains = []string{contractDomain}
	grant.Networks = []string{"127.0.0.1"}
	grant.Launchers = []string{"demo"}
	p.Principals["user"] = grant
	r := route{Owner: "user", Domain: contractDomain, Launcher: "demo", routeInput: routeInput{Name: "Original", Port: 8080}}
	return p, r, file
}

func TestConsoleLegacyAppOwnershipFollowsStartupContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      string
		autoStart bool
		owner     string
		stop      int
	}{
		{"copy only", "[]", true, "user", http.StatusOK},
		{"startup arguments", `["changed"]`, true, "admin", http.StatusForbidden},
		{"autostart", "[]", false, "admin", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			p, r, file := contractLegacyApp(t)
			_, handler := contractTestServer(t, p, []route{r})
			member := "/routes/" + contractDomain
			body := fmt.Sprintf(`{"domain":%q,"name":"Updated","description":"New copy","startFile":%q,"args":%s,"port":8080,"autoStart":%t}`, contractDomain, file, test.args, test.autoStart)
			contractRequest(t, handler, contractAdminToken, http.MethodPut, "/admin"+member, body, http.StatusNoContent)
			w := contractRequest(t, handler, contractAdminToken, http.MethodGet, "/api"+member, "", http.StatusOK)
			var got RouteResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Owner != test.owner || got.Name != "Updated" || got.Description != "New copy" || got.AutoStart == nil || *got.AutoStart != test.autoStart {
				t.Fatalf("updated route = %s; want owner %q, new copy and autostart=%t", w.Body, test.owner, test.autoStart)
			}
			contractRequest(t, handler, contractUserToken, http.MethodPost, "/api"+member+"/stop", "{}", test.stop)
		})
	}
}

func TestConsoleLegacyLauncherGrantsCanBeClearedExplicitly(t *testing.T) {
	p, r, _ := contractLegacyApp(t)
	_, handler := contractTestServer(t, p, []route{r})
	const identity = "/admin/principals/user"
	const scoped = `"create":false,"admin":false,"domains":["demo.example.test"],"networks":["127.0.0.1"]`
	stop := "/api/routes/" + contractDomain + "/stop"
	contractRequest(t, handler, contractAdminToken, http.MethodPut, identity, `{`+scoped+`}`, http.StatusNoContent)
	contractRequest(t, handler, contractUserToken, http.MethodPost, stop, "{}", http.StatusOK)
	contractRequest(t, handler, contractAdminToken, http.MethodPut, identity, `{`+scoped+`,"launchers":["unknown"]}`, http.StatusBadRequest)
	contractRequest(t, handler, contractUserToken, http.MethodPost, stop, "{}", http.StatusOK)
	contractRequest(t, handler, contractAdminToken, http.MethodPut, identity, `{`+scoped+`,"launchers":[]}`, http.StatusNoContent)
	contractRequest(t, handler, contractUserToken, http.MethodPost, stop, "{}", http.StatusForbidden)
	contractRequest(t, handler, contractAdminToken, http.MethodPut, identity, `{`+scoped+`,"launchers":["demo"]}`, http.StatusNoContent)
	contractRequest(t, handler, contractUserToken, http.MethodPost, stop, "{}", http.StatusOK)
}

func TestPolicyChangePrunesInactiveIssuerKeys(t *testing.T) {
	for _, name := range []string{"rotate", "downgrade", "reload"} {
		t.Run(name, func(t *testing.T) {
			p := contractTestPolicy()
			s, handler := contractTestServer(t, p, nil)
			const app = `{"domain":"demo.example.test","address":"127.0.0.1:8080","name":"Demo"}`
			adminKey := sha256.Sum256([]byte(contractIssueKey(t, handler, contractAdminToken, app)))
			userKey := sha256.Sum256([]byte(contractIssueKey(t, handler, contractUserToken, app)))
			switch name {
			case "rotate":
				contractRequest(t, handler, contractAdminToken, http.MethodPost, "/admin/principals/user/rotate", `{"token":"a-new-console-user-token"}`, http.StatusNoContent)
				contractRequest(t, handler, contractAdminToken, http.MethodPost, "/admin/principals/user/rotate", `{"token":"`+contractUserToken+`"}`, http.StatusNoContent)
			case "downgrade":
				contractRequest(t, handler, contractAdminToken, http.MethodPut, "/admin/principals/user", `{"create":false,"admin":false,"domains":["demo.example.test"],"networks":["127.0.0.1"]}`, http.StatusNoContent)
				contractRequest(t, handler, contractAdminToken, http.MethodPut, "/admin/principals/user", `{"create":false,"admin":true}`, http.StatusNoContent)
			case "reload":
				user := p.Principals["user"]
				delete(p.Principals, "user")
				if err := writeJSONAtomic(s.configPath, &p, maxPolicyBytes); err != nil {
					t.Fatal(err)
				}
				if err := s.reload(); err != nil {
					t.Fatal(err)
				}
				p.Principals["user"] = user
				if err := writeJSONAtomic(s.configPath, &p, maxPolicyBytes); err != nil {
					t.Fatal(err)
				}
				if err := s.reload(); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := s.deployKeys[userKey]; ok {
				t.Fatal("inactive issuer's key survived policy change and restoration")
			}
			if _, ok := s.deployKeys[adminKey]; !ok {
				t.Fatal("active issuer's key was removed by an unrelated policy change")
			}
		})
	}
}

func TestInvalidDeploymentConfigurationReturnsPublicError(t *testing.T) {
	_, handler := contractTestServer(t, contractTestPolicy(), nil)
	privateFile := filepath.Join(t.TempDir(), "private-start-file.exe")
	if err := os.WriteFile(privateFile, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	token := contractIssueKey(t, handler, contractAdminToken, fmt.Sprintf(`{"domain":%q,"startFile":%q}`, contractDomain, privateFile))
	if err := os.Remove(privateFile); err != nil {
		t.Fatal(err)
	}
	w := contractRequest(t, handler, token, http.MethodPost, "/api/routes", `{"domain":"demo.example.test"}`, http.StatusBadRequest)
	var got apiProblem
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Error != "预设部署配置已失效，请联系签发人" || strings.Contains(w.Body.String(), filepath.Base(privateFile)) {
		t.Fatal("expected only the public configuration error")
	}
}
