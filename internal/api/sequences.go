package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ditto/internal/eventtemplate"
	"ditto/internal/jsonfile"
	"ditto/internal/sequence"
	"ditto/internal/socket"
)

const maxEventSequenceBodyBytes = 1 << 20

type eventSequencesResponse struct {
	Sequences []sequence.Sequence `json:"sequences"`
}

type sequencePlayRequest struct {
	Vars      map[string]json.RawMessage `json:"vars,omitempty"`
	StartStep *int                       `json:"start_step,omitempty"`
	Speed     *float64                   `json:"speed,omitempty"`
}

type sequenceSeekRequest struct {
	Step int `json:"step"`
}

type sequenceSpeedRequest struct {
	Speed float64 `json:"speed"`
}

// sequence.NewStore loads the sequences in dir. Sequences that reference
// unknown templates, adapters or schema types are skipped, so those
// registries must be loaded first.

func decodeEventSequenceJSON(w http.ResponseWriter, r *http.Request, dst any, allowEmpty bool) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventSequenceBodyBytes))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "failed to read request body: "+err.Error(), http.StatusBadRequest)
		}
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		if allowEmpty {
			return true
		}
		http.Error(w, "request body is required", http.StatusBadRequest)
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func splitSequencePath(rest string) (id string, action string, ok bool) {
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 1 && jsonfile.ValidID(parts[0]) {
		return parts[0], "", true
	}
	if len(parts) == 2 && jsonfile.ValidID(parts[0]) {
		switch parts[1] {
		case "play", "pause", "stop", "seek", "speed":
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

func registerSequenceRoutes(mux *http.ServeMux, registry *sequence.Store, player *sequence.Player, broadcaster *sequence.Broadcaster) {
	if registry == nil || player == nil {
		return
	}
	mux.HandleFunc("/__ditto__/api/sequences", func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(eventSequencesResponse{Sequences: registry.Sequences()})
		case http.MethodPost:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var seq sequence.Sequence
			if ok := decodeEventSequenceJSON(w, r, &seq, false); !ok {
				return
			}
			created, err := registry.Create(seq)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(created)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/__ditto__/api/sequences/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"states": player.States()})
	})
	mux.HandleFunc("/__ditto__/api/sequences/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		if broadcaster == nil {
			http.Error(w, "player broadcaster is not available", http.StatusInternalServerError)
			return
		}
		broadcaster.ServeHTTP(w, r)
	})
	mux.HandleFunc("/__ditto__/api/sequences/", func(w http.ResponseWriter, r *http.Request) {
		if !socket.OriginAllowed(r) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/__ditto__/api/sequences/")
		id, action, ok := splitSequencePath(rest)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if action != "" {
			handleSequencePlayerAction(w, r, player, id, action)
			return
		}
		switch r.Method {
		case http.MethodGet:
			seq, err := registry.Get(id)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(seq)
		case http.MethodPut:
			if !hasJSONContentType(r) {
				http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
			var seq sequence.Sequence
			if ok := decodeEventSequenceJSON(w, r, &seq, false); !ok {
				return
			}
			updated, err := registry.Update(id, seq)
			if err != nil {
				status := http.StatusBadRequest
				if errors.Is(err, sequence.ErrNotFound) {
					status = http.StatusNotFound
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(updated)
		case http.MethodDelete:
			if err := player.DeleteWhenIdle(id, func() error {
				return registry.Delete(id)
			}); err != nil {
				status := http.StatusNotFound
				if errors.Is(err, sequence.ErrActive) {
					status = http.StatusConflict
				}
				http.Error(w, err.Error(), status)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func handleSequencePlayerAction(w http.ResponseWriter, r *http.Request, player *sequence.Player, id string, action string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if requestHasBody(r) && !hasJSONContentType(r) {
		http.Error(w, "content-type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	var state sequence.State
	var err error
	switch action {
	case "play":
		var req sequencePlayRequest
		if ok := decodeEventSequenceJSON(w, r, &req, true); !ok {
			return
		}
		vars, varErr := eventtemplate.VarsFromJSON(req.Vars)
		if varErr != nil {
			http.Error(w, varErr.Error(), http.StatusBadRequest)
			return
		}
		opts := sequence.PlayOptions{Vars: vars}
		if req.StartStep != nil {
			opts.StartStep = *req.StartStep
		}
		if req.Speed != nil {
			opts.Speed = *req.Speed
			opts.SpeedSet = true
		}
		state, err = player.Play(id, opts)
	case "pause":
		state, err = player.Pause(id)
	case "stop":
		state, err = player.Stop(id)
	case "seek":
		var req sequenceSeekRequest
		if ok := decodeEventSequenceJSON(w, r, &req, false); !ok {
			return
		}
		state, err = player.Seek(id, req.Step)
	case "speed":
		var req sequenceSpeedRequest
		if ok := decodeEventSequenceJSON(w, r, &req, false); !ok {
			return
		}
		state, err = player.SetSpeed(id, req.Speed)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sequence.ErrNotFound) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

func requestHasBody(r *http.Request) bool {
	return r.Body != nil && r.ContentLength != 0
}
