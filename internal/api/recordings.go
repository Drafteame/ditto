package api

import (
	"net/http"
	"strconv"

	"ditto/internal/schema"
)

func (a *API) listRecordings(w http.ResponseWriter, r *http.Request) {
	items, err := a.Recorder.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recordings": items, "active_id": a.Recorder.ActiveID()})
}

func (a *API) startRecording(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if !readJSON(w, r, &req, true) {
		return
	}
	manifest, err := a.Recorder.Start(req.Name, req.Description)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, manifest)
}

func (a *API) getRecording(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	manifest, err := a.Recorder.Manifest(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (a *API) deleteRecording(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.Recorder.Delete(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) stopRecording(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	manifest, err := a.Recorder.Stop(id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

// recordingFrames returns a page of one channel's frames: ?channel=,
// ?offset= and ?limit=.
func (a *API) recordingFrames(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	offset, _ := strconv.Atoi(query.Get("offset"))
	limit, _ := strconv.Atoi(query.Get("limit"))
	frames, err := a.Recorder.Frames(id, query.Get("channel"), offset, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"frames": frames, "offset": offset, "limit": limit})
}

func (a *API) listSchemaPacks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"packs": a.Schemas.Packs()})
}

// uploadSchemaPack imports a .proto file or .zip pack sent as the "pack"
// field of a multipart form.
func (a *API) uploadSchemaPack(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, schema.MaxUploadBytes)
	if err := r.ParseMultipartForm(schema.MaxUploadBytes); err != nil {
		http.Error(w, "invalid multipart upload: "+err.Error(), statusFor(err))
		return
	}
	file, header, err := r.FormFile("pack")
	if err != nil {
		http.Error(w, `missing upload field "pack"`, http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }() // read-only
	pack, err := a.Schemas.Import(header.Filename, file)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, pack)
}

func (a *API) deleteSchemaPack(w http.ResponseWriter, r *http.Request) {
	if err := a.Schemas.DeletePack(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listSchemaTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"types": a.Schemas.Types()})
}
