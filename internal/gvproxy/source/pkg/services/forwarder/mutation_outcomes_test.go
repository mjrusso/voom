//go:build ignore

// Added by the Voom project; see https://github.com/mjrusso/voom.
package forwarder

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMutationOutcomeFencesDelayedHandler(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := TrackMutationOutcomes(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		http.Error(w, "failed to bind", http.StatusConflict)
	}))
	request := httptest.NewRequest(http.MethodPost, "/expose", nil)
	request.Header.Set("X-Voom-Forward-ID", "delayed")
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), request) }()
	<-entered
	query := func(id string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/outcome?id="+id, nil))
		return response
	}
	if response := query("delayed"); response.Code != http.StatusAccepted {
		t.Fatal(response)
	}
	if response := query("missing"); response.Code != http.StatusNotFound {
		t.Fatal(response)
	}
	duplicate := httptest.NewRecorder()
	h.ServeHTTP(duplicate, request.Clone(request.Context()))
	if duplicate.Code != http.StatusConflict {
		t.Fatal(duplicate)
	}
	close(release)
	<-done
	response := query("delayed")
	var outcome mutationOutcome
	if err := json.Unmarshal(response.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || outcome.Status != http.StatusConflict || outcome.Body != "failed to bind\n" {
		t.Fatalf("outcome: %+v", outcome)
	}
}

func TestMutationOutcomeCacheIsBounded(t *testing.T) {
	h := TrackMutationOutcomes(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	for i := range 1024 {
		id := fmt.Sprint(i)
		request := httptest.NewRequest(http.MethodPost, "/unexpose", nil)
		request.Header.Set("X-Voom-Forward-ID", id)
		h.ServeHTTP(httptest.NewRecorder(), request)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/outcome?id="+id, nil))
		if response.Code != http.StatusOK {
			t.Fatal(response)
		}
	}
	retained := 0
	for i := range 1024 {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/outcome?id="+fmt.Sprint(i), nil))
		if response.Code == http.StatusOK {
			retained++
		}
	}
	if retained > 256 {
		t.Fatalf("retained %d completed outcomes", retained)
	}
}
