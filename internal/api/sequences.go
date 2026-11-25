package api

import (
	"encoding/json"
	"net/http"

	"ditto/internal/eventtemplate"
	"ditto/internal/sequence"
)

type sequencePlayRequest struct {
	Vars      map[string]json.RawMessage `json:"vars,omitempty"`
	StartStep *int                       `json:"start_step,omitempty"`
	Speed     *float64                   `json:"speed,omitempty"`
}

func (a *API) listSequences(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sequences": a.Sequences.Sequences()})
}

func (a *API) createSequence(w http.ResponseWriter, r *http.Request) {
	var seq sequence.Sequence
	if !readJSON(w, r, &seq, true) {
		return
	}
	created, err := a.Sequences.Create(seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) sequenceStates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"states": a.Player.States()})
}

// streamSequenceEvents streams player events as server-sent events.
func (a *API) streamSequenceEvents(w http.ResponseWriter, r *http.Request) {
	if a.Broadcaster == nil {
		http.Error(w, "player broadcaster is not available", http.StatusInternalServerError)
		return
	}
	a.Broadcaster.ServeHTTP(w, r)
}

func (a *API) getSequence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	seq, err := a.Sequences.Get(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, seq)
}

func (a *API) updateSequence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var seq sequence.Sequence
	if !readJSON(w, r, &seq, true) {
		return
	}
	updated, err := a.Sequences.Update(id, seq)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// deleteSequence deletes a sequence unless it is playing or paused (409).
func (a *API) deleteSequence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := a.Player.DeleteWhenIdle(id, func() error { return a.Sequences.Delete(id) })
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// playSequence starts or resumes a sequence with optional runtime
// variables, start step and speed.
func (a *API) playSequence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req sequencePlayRequest
	if !readJSON(w, r, &req, false) {
		return
	}
	vars, err := eventtemplate.VarsFromJSON(req.Vars)
	if err != nil {
		writeError(w, err)
		return
	}
	opts := sequence.PlayOptions{Vars: vars}
	if req.StartStep != nil {
		opts.StartStep = *req.StartStep
	}
	if req.Speed != nil {
		opts.Speed, opts.SpeedSet = *req.Speed, true
	}
	writeState(w)(a.Player.Play(id, opts))
}

func (a *API) pauseSequence(w http.ResponseWriter, r *http.Request) {
	if id, ok := pathID(w, r); ok {
		writeState(w)(a.Player.Pause(id))
	}
}

func (a *API) stopSequence(w http.ResponseWriter, r *http.Request) {
	if id, ok := pathID(w, r); ok {
		writeState(w)(a.Player.Stop(id))
	}
}

func (a *API) seekSequence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Step int `json:"step"`
	}
	if readJSON(w, r, &req, true) {
		writeState(w)(a.Player.Seek(id, req.Step))
	}
}

func (a *API) setSequenceSpeed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Speed float64 `json:"speed"`
	}
	if readJSON(w, r, &req, true) {
		writeState(w)(a.Player.SetSpeed(id, req.Speed))
	}
}

// writeState answers a player command with its resulting state or error.
func writeState(w http.ResponseWriter) func(sequence.State, error) {
	return func(state sequence.State, err error) {
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, state)
	}
}
