// Added by the Voom project; see https://github.com/mjrusso/voom.

// Package forwarder tracks requests to gvproxy's host forwarder.
package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
)

type mutationOutcome struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type mutationRecord struct {
	fingerprint  [32]byte
	outcome      *mutationOutcome
	acknowledged bool
}

type outcomeWriter struct {
	header  http.Header
	outcome mutationOutcome
}

func (w *outcomeWriter) Header() http.Header { return w.header }
func (w *outcomeWriter) WriteHeader(status int) {
	if w.outcome.Status == 0 {
		w.outcome.Status = status
	}
}
func (w *outcomeWriter) Write(body []byte) (int, error) {
	if w.outcome.Status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	remaining := 4096 - len(w.outcome.Body)
	w.outcome.Body += string(body[:min(len(body), remaining)])
	return len(body), nil
}

func writeMutationOutcome(w http.ResponseWriter, outcome *mutationOutcome) {
	w.Header().Set("X-Voom-Forward-Outcome", "completed")
	w.WriteHeader(outcome.Status)
	_, _ = io.WriteString(w, outcome.Body)
}

// TrackMutationOutcomes deduplicates forwarding requests and retains their results.
// Completion is published only after the forwarding handler returns.
func TrackMutationOutcomes(next http.Handler) http.Handler {
	var mu sync.Mutex
	records := make(map[uint64]*mutationRecord)
	var issued, retired uint64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ids" {
			count, err := strconv.ParseUint(r.URL.Query().Get("count"), 10, 64)
			if r.Method != http.MethodPost || err != nil || count == 0 {
				http.Error(w, "positive count and POST required", http.StatusBadRequest)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if count > math.MaxUint64-issued {
				http.Error(w, "forwarding request IDs exhausted", http.StatusBadRequest)
				return
			}
			first := issued + 1
			issued += count
			_ = json.NewEncoder(w).Encode(struct {
				First uint64 `json:"first"`
			}{first})
			return
		}
		if r.URL.Path == "/acknowledge" || r.URL.Path == "/outcome" {
			id, err := strconv.ParseUint(r.URL.Query().Get("id"), 10, 64)
			if err != nil || id == 0 {
				http.Error(w, "invalid mutation ID", http.StatusBadRequest)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			record, found := records[id]
			if r.URL.Path == "/outcome" {
				if r.Method != http.MethodGet {
					http.Error(w, "get only", http.StatusBadRequest)
				} else if id <= retired {
					w.Header().Set("X-Voom-Forward-Outcome", "retired")
					http.Error(w, "mutation ID retired", http.StatusGone)
				} else if !found {
					http.NotFound(w, r)
				} else if record.outcome == nil {
					w.WriteHeader(http.StatusAccepted)
				} else {
					_ = json.NewEncoder(w).Encode(record.outcome)
				}
				return
			}
			if r.Method != http.MethodPost {
				http.Error(w, "post only", http.StatusBadRequest)
				return
			}
			if found && record.outcome == nil {
				http.Error(w, "mutation is still pending", http.StatusConflict)
				return
			}
			if found {
				record.acknowledged = true
				record.outcome = &mutationOutcome{Status: record.outcome.Status}
			}
			// Unreceived IDs below the watermark are fenced too. A late
			// submission must not execute after a newer request was retired.
			through, firstUnacknowledged := retired, uint64(0)
			for id, record := range records {
				if record.acknowledged {
					through = max(through, id)
				} else if firstUnacknowledged == 0 || id < firstUnacknowledged {
					firstUnacknowledged = id
				}
			}
			if firstUnacknowledged != 0 {
				through = min(through, firstUnacknowledged-1)
			}
			retired = through
			for id := range records {
				if id <= retired {
					delete(records, id)
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rawID := r.Header.Get("X-Voom-Forward-ID")
		if rawID == "" || r.Method != http.MethodPost || (r.URL.Path != "/expose" && r.URL.Path != "/unexpose") {
			next.ServeHTTP(w, r)
			return
		}
		reject := func(message string, status int) {
			w.Header().Set("X-Voom-Forward-Outcome", "not-submitted")
			http.Error(w, message, status)
		}
		id, parseErr := strconv.ParseUint(rawID, 10, 64)
		if parseErr != nil || id == 0 {
			reject("invalid mutation ID", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil || len(body) > 4096 {
			reject("invalid mutation body", http.StatusBadRequest)
			return
		}
		fingerprint := sha256.Sum256(append([]byte(r.URL.Path+"\x00"), body...))
		r.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		if id <= retired {
			mu.Unlock()
			w.Header().Set("X-Voom-Forward-Outcome", "retired")
			http.Error(w, "mutation ID retired", http.StatusGone)
			return
		}
		if id > issued {
			mu.Unlock()
			reject("mutation ID was not allocated", http.StatusBadRequest)
			return
		}
		if record, found := records[id]; found {
			outcome := record.outcome
			same := record.fingerprint == fingerprint
			mu.Unlock()
			if !same {
				reject("mutation ID used with a different request", http.StatusUnprocessableEntity)
			} else if outcome == nil {
				w.Header().Set("X-Voom-Forward-Outcome", "pending")
				w.WriteHeader(http.StatusAccepted)
			} else {
				writeMutationOutcome(w, outcome)
			}
			return
		}
		if len(records) >= 256 {
			mu.Unlock()
			reject("forwarding request history is full", http.StatusTooManyRequests)
			return
		}
		record := &mutationRecord{fingerprint: fingerprint}
		records[id] = record
		mu.Unlock()
		capture := &outcomeWriter{header: make(http.Header)}
		next.ServeHTTP(capture, r)
		if capture.outcome.Status == 0 {
			capture.outcome.Status = http.StatusOK
		}
		mu.Lock()
		record.outcome = &capture.outcome
		mu.Unlock()
		for name, values := range capture.header {
			w.Header()[name] = values
		}
		writeMutationOutcome(w, &capture.outcome)
	})
}
