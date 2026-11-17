//go:build ignore

// Added by the Voom project; see https://github.com/mjrusso/voom.
package forwarder

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMutationOutcomeFencesDelayedHandler(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := newMutationOutcomeHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		http.Error(w, "failed to bind", http.StatusConflict)
	}))
	request := httptest.NewRequest(http.MethodPost, "/expose", nil)
	request.Header.Set("X-Voom-Forward-ID", "1")
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), request) }()
	<-entered
	query := func(id string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/outcome?id="+id, nil))
		return response
	}
	if response := query("1"); response.Code != http.StatusAccepted {
		t.Fatal(response)
	}
	if response := query("2"); response.Code != http.StatusNotFound {
		t.Fatal(response)
	}
	ack := httptest.NewRecorder()
	h.ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/acknowledge?id=1", nil))
	if ack.Code != http.StatusConflict || query("1").Code != http.StatusAccepted {
		t.Fatal("acknowledgement discarded a pending mutation")
	}
	duplicate := httptest.NewRecorder()
	h.ServeHTTP(duplicate, request.Clone(request.Context()))
	if duplicate.Code != http.StatusAccepted {
		t.Fatal(duplicate)
	}
	close(release)
	<-done
	response := query("1")
	var outcome mutationOutcome
	if err := json.Unmarshal(response.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || outcome.Status != http.StatusConflict || outcome.Body != "failed to bind\n" {
		t.Fatalf("outcome: %+v", outcome)
	}
}

func TestMutationOutcomesRequireAcknowledgement(t *testing.T) {
	calls := 0
	h := newMutationOutcomeHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	mutate := func(id string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/expose", nil)
		request.Header.Set("X-Voom-Forward-ID", id)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response
	}
	for i := range 256 {
		if response := mutate(fmt.Sprint(i + 1)); response.Code != http.StatusOK {
			t.Fatal(response)
		}
	}
	if response := mutate("257"); response.Code != http.StatusTooManyRequests || calls != 256 {
		t.Fatalf("capacity rejection: HTTP %d, mutations=%d", response.Code, calls)
	}
	for i := range 256 {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/outcome?id="+fmt.Sprint(i+1), nil))
		if response.Code != http.StatusOK {
			t.Fatalf("unacknowledged result lost: %v", response)
		}
	}
	for range 2 {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/acknowledge?id=1", nil))
		if response.Code != http.StatusNoContent {
			t.Fatal(response)
		}
	}
	if response := mutate("257"); response.Code != http.StatusOK || calls != 257 {
		t.Fatalf("capacity after acknowledgement: HTTP %d, mutations=%d", response.Code, calls)
	}
}

func TestMutationIDsDeduplicatePendingCompletedAndAcknowledgedRequests(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	h := newMutationOutcomeHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		close(entered)
		<-release
		http.Error(w, "failed bind", http.StatusConflict)
	}))
	mutate := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/expose", strings.NewReader(body))
		r.Header.Set("X-Voom-Forward-ID", "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	go func() { defer close(done); mutate(`{"local":"127.0.0.1:8080"}`) }()
	<-entered
	var released sync.Once
	defer func() { released.Do(func() { close(release) }); <-done }()
	if got := mutate(`{"local":"127.0.0.1:8080"}`); got.Code != http.StatusAccepted {
		t.Fatalf("pending duplicate: %d, %s", got.Code, got.Body)
	}
	released.Do(func() { close(release) })
	<-done
	if got := mutate(`{"local":"127.0.0.1:8080"}`); got.Code != http.StatusConflict || got.Body.String() != "failed bind\n" || calls != 1 {
		t.Fatalf("completed duplicate: %d, %s, calls=%d", got.Code, got.Body, calls)
	}
	if got := mutate(`{"local":"127.0.0.1:9090"}`); got.Code != http.StatusUnprocessableEntity || calls != 1 {
		t.Fatalf("changed payload: %d, calls=%d", got.Code, calls)
	}
	ack := httptest.NewRecorder()
	h.ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/acknowledge?id=1", nil))
	if got := mutate(`{"local":"127.0.0.1:8080"}`); got.Code != http.StatusGone || calls != 1 {
		t.Fatalf("late duplicate after acknowledgement: %d, calls=%d", got.Code, calls)
	}
}

func TestConcurrentFirstSubmissionsExecuteOnce(t *testing.T) {
	entered, release := make(chan struct{}, 16), make(chan struct{})
	var calls atomic.Int32
	h := newMutationOutcomeHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	start := make(chan struct{})
	responses := make(chan int, 16)
	var once sync.Once
	defer once.Do(func() { close(release) })
	for range 16 {
		go func() {
			<-start
			r := httptest.NewRequest(http.MethodPost, "/expose", nil)
			r.Header.Set("X-Voom-Forward-ID", "1")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			responses <- w.Code
		}()
	}
	close(start)
	<-entered
	for range 15 {
		select {
		case code := <-responses:
			if code != http.StatusAccepted {
				t.Fatalf("duplicate: HTTP %d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("duplicate submissions executed concurrently")
		}
	}
	once.Do(func() { close(release) })
	if code := <-responses; code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("completed: HTTP %d, executions=%d", code, calls.Load())
	}
}

func TestAcknowledgedChurnRetiresOldIDs(t *testing.T) {
	calls := 0
	h := newMutationOutcomeHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) }))
	for id := 1; id <= 4096; id++ {
		r := httptest.NewRequest(http.MethodPost, "/expose", nil)
		r.Header.Set("X-Voom-Forward-ID", fmt.Sprint(id))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("mutation %d: HTTP %d", id, w.Code)
		}
		ack := httptest.NewRecorder()
		h.ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/acknowledge?id="+fmt.Sprint(id), nil))
		if ack.Code != http.StatusNoContent {
			t.Fatal(ack)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/outcome?id=1", nil))
	if w.Code != http.StatusGone {
		t.Fatalf("acknowledged result was retained: HTTP %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodPost, "/expose", nil)
	r.Header.Set("X-Voom-Forward-ID", "1")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusGone || calls != 4096 {
		t.Fatalf("retired submission: HTTP %d, executions=%d", w.Code, calls)
	}
}

func TestOutOfOrderAcknowledgementsRemainBounded(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	h := newMutationOutcomeHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Voom-Forward-ID") == "1" {
			close(entered)
			<-release
		}
		w.WriteHeader(http.StatusOK)
	}))
	mutate := func(id int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/expose", nil)
		r.Header.Set("X-Voom-Forward-ID", fmt.Sprint(id))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	var once sync.Once
	defer func() { once.Do(func() { close(release) }); <-done }()
	go func() { defer close(done); mutate(1) }()
	<-entered
	for id := 2; id <= 256; id++ {
		if w := mutate(id); w.Code != http.StatusOK {
			t.Fatal(w)
		}
		ack := httptest.NewRecorder()
		h.ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/acknowledge?id="+fmt.Sprint(id), nil))
		if ack.Code != http.StatusNoContent {
			t.Fatal(ack)
		}
	}
	if w := mutate(257); w.Code != http.StatusTooManyRequests {
		t.Fatalf("pending gap allowed unbounded acknowledged records: HTTP %d", w.Code)
	}
	once.Do(func() { close(release) })
	<-done
	ack := httptest.NewRecorder()
	h.ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/acknowledge?id=1", nil))
	if w := mutate(257); w.Code != http.StatusOK {
		t.Fatalf("closing the gap did not release records: HTTP %d", w.Code)
	}
}

func newMutationOutcomeHandler(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	h := TrackMutationOutcomes(next)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ids?count=8192", nil))
	var allocated struct {
		First uint64 `json:"first"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &allocated); err != nil || w.Code != http.StatusOK || allocated.First != 1 {
		t.Fatalf("allocate IDs: HTTP %d, %s, %v", w.Code, w.Body, err)
	}
	return h
}

func TestRetirementFencesUnreceivedIDs(t *testing.T) {
	calls := 0
	h := newMutationOutcomeHandler(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) }))
	mutate := func(id string) int {
		r := httptest.NewRequest(http.MethodPost, "/expose", nil)
		r.Header.Set("X-Voom-Forward-ID", id)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := mutate("2"); code != http.StatusOK {
		t.Fatal(code)
	}
	ack := httptest.NewRecorder()
	h.ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/acknowledge?id=2", nil))
	if code := mutate("1"); code != http.StatusGone || calls != 1 {
		t.Fatalf("unreceived ID crossed retirement: HTTP %d, calls=%d", code, calls)
	}
	if code := mutate("3"); code != http.StatusOK || calls != 2 {
		t.Fatalf("new ID could not execute: HTTP %d, calls=%d", code, calls)
	}
}

func TestMutationIDAllocationsDoNotOverlap(t *testing.T) {
	h := TrackMutationOutcomes(http.NotFoundHandler())
	for _, allocation := range []struct{ count, first uint64 }{{2, 1}, {3, 3}, {1, 6}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, fmt.Sprintf("/ids?count=%d", allocation.count), nil))
		var result struct {
			First uint64 `json:"first"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result.First != allocation.first {
			t.Fatalf("allocate %d: HTTP %d, %s, %v", allocation.count, w.Code, w.Body, err)
		}
	}
}
