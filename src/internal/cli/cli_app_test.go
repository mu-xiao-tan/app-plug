package cli_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mu-xiao-tan/app-plug/src/internal/cli"
)

func TestAppUpdateReturnsDeploymentRejection(t *testing.T) {
	const member = "/api/routes/demo.example.test"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == member:
			fmt.Fprint(w, `{"domain":"demo.example.test","name":"Original","address":"127.0.0.1:8080"}`)
		case r.Method == http.MethodPut && r.URL.Path == member:
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":"update rejected by fixture"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"unexpected endpoint"}`)
		}
	}))
	t.Cleanup(remote.Close)
	t.Setenv("APP_PLUG_TOKEN", "test-token-valid-syntax")

	err := cli.Run([]string{"app", "update", "--url", remote.URL, "--domain", "demo.example.test", "--name", "New name"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 409：update rejected by fixture") {
		t.Fatalf("app update returned %v; want the PUT rejection (HTTP 409)", err)
	}
}
