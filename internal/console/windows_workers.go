package console

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/akayami/runner-orchard/internal/fleet"
)

// WindowsWorker binds one lifecycle credential to an immutable GitHub runner ID.
type WindowsWorker struct {
	ID           string `json:"id"`
	Organization string `json:"organization"`
	RunnerID     int64  `json:"runner_id"`
	TokenHash    string `json:"token_sha256"`
}

func validateWindowsWorkers(workers []WindowsWorker) error {
	if len(workers) > 32 {
		return fmt.Errorf("too many Windows worker bindings")
	}
	ids, runners, hashes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, worker := range workers {
		key := fmt.Sprintf("%s/%d", strings.ToLower(worker.Organization), worker.RunnerID)
		hash := strings.ToLower(worker.TokenHash)
		decoded, err := hex.DecodeString(hash)
		if !safeName.MatchString(worker.ID) || !safeName.MatchString(worker.Organization) ||
			worker.RunnerID <= 0 || err != nil || len(decoded) != sha256.Size ||
			ids[worker.ID] || runners[key] || hashes[hash] {
			return fmt.Errorf("invalid or duplicate Windows worker binding")
		}
		ids[worker.ID], runners[key], hashes[hash] = true, true, true
	}
	return nil
}

func registerWindowsWorkers(mux *http.ServeMux, bindings []WindowsWorker, github *fleet.GitHub) {
	if github == nil || len(bindings) == 0 {
		return
	}
	workers := make(map[string]WindowsWorker, len(bindings))
	locks := make(map[string]*sync.Mutex, len(bindings))
	for _, worker := range bindings {
		workers[worker.ID] = worker
		locks[worker.ID] = new(sync.Mutex)
	}
	mux.HandleFunc("POST /api/windows-workers/{id}", func(w http.ResponseWriter, r *http.Request) {
		worker, found := workers[r.PathValue("id")]
		token, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found || !bearer || len(token) != 64 {
			http.Error(w, "invalid worker authorization", http.StatusUnauthorized)
			return
		}
		raw, err := hex.DecodeString(token)
		expected, _ := hex.DecodeString(worker.TokenHash)
		hash := sha256.Sum256(raw)
		if err != nil || subtle.ConstantTimeCompare(hash[:], expected) != 1 {
			http.Error(w, "invalid worker authorization", http.StatusUnauthorized)
			return
		}
		var input struct {
			Eligible *bool `json:"eligible"`
		}
		if err := decodeBody(r, &input); err != nil || input.Eligible == nil {
			http.Error(w, "invalid lifecycle request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		lock := locks[worker.ID]
		if !lock.TryLock() {
			http.Error(w, "worker lifecycle request already active", http.StatusConflict)
			return
		}
		defer lock.Unlock()
		result, err := github.SetWindowsEligibility(ctx, worker.Organization, worker.RunnerID, *input.Eligible)
		if err != nil {
			http.Error(w, "GitHub worker control unavailable", http.StatusBadGateway)
			return
		}
		jsonResponse(w, result)
	})
}
