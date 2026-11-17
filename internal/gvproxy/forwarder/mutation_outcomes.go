// Added by the Voom project; see https://github.com/mjrusso/voom.
package forwarder

import (
	"encoding/json"
	"net/http"
	"sync"
)

type mutationOutcome struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

type outcomeWriter struct {
	http.ResponseWriter
	outcome mutationOutcome
}

func (w *outcomeWriter) WriteHeader(status int) {
	if w.outcome.Status != 0 {
		return
	}
	w.outcome.Status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *outcomeWriter) Write(body []byte) (int, error) {
	if w.outcome.Status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	remaining := 4096 - len(w.outcome.Body)
	w.outcome.Body += string(body[:min(len(body), remaining)])
	return w.ResponseWriter.Write(body)
}

// An outcome is published only after the mutation handler returns.
// A forwarding-table snapshot can be taken while an expose is still pending.
// Missing or evicted IDs never establish completion. Only completed entries
// can be evicted.
func TrackMutationOutcomes(next http.Handler) http.Handler {
	var mu sync.Mutex
	outcomes := make(map[string]*mutationOutcome)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/outcome" {
			if r.Method != http.MethodGet {
				http.Error(w, "get only", http.StatusBadRequest)
				return
			}
			mu.Lock()
			result, found := outcomes[r.URL.Query().Get("id")]
			mu.Unlock()
			if !found {
				http.NotFound(w, r)
				return
			}
			if result == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		id := r.Header.Get("X-Voom-Forward-ID")
		if id == "" || r.Method != http.MethodPost || (r.URL.Path != "/expose" && r.URL.Path != "/unexpose") {
			next.ServeHTTP(w, r)
			return
		}
		if len(id) > 64 {
			http.Error(w, "invalid mutation ID", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if _, found := outcomes[id]; found {
			mu.Unlock()
			http.Error(w, "mutation ID already used", http.StatusConflict)
			return
		}
		if len(outcomes) >= 256 {
			for old, result := range outcomes {
				if result != nil {
					delete(outcomes, old)
					break
				}
			}
		}
		if len(outcomes) >= 256 {
			mu.Unlock()
			http.Error(w, "too many pending mutations", http.StatusServiceUnavailable)
			return
		}
		outcomes[id] = nil
		mu.Unlock()
		capture := &outcomeWriter{ResponseWriter: w}
		next.ServeHTTP(capture, r)
		if capture.outcome.Status == 0 {
			capture.outcome.Status = http.StatusOK
		}
		mu.Lock()
		outcomes[id] = &capture.outcome
		mu.Unlock()
	})
}
