//go:build ignore

package forwarder

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnexposeMissingIsIdempotent(t *testing.T) {
	f := &PortsForwarder{proxies: map[ProxyKey]proxy{}}
	request := httptest.NewRequest(http.MethodPost, "/unexpose", strings.NewReader(`{"local":"127.0.0.1:8080","protocol":"tcp"}`))
	response := httptest.NewRecorder()
	f.Mux().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
}
