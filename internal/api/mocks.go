package api

import (
	"net"
	"net/http"
	"strconv"

	"ditto/internal/httpmock"
)

type mocksResponse struct {
	Mocks []httpmock.Mock `json:"mocks"`
	Info  Info            `json:"info"`
}

type disabledDuplicates struct {
	DisabledDuplicates []string `json:"disabled_duplicates"`
}

// listMocks returns every mock with the server info the dashboard shows.
func (a *API) listMocks(w http.ResponseWriter, r *http.Request) {
	info := a.currentInfo()
	// The port the client reached is authoritative after a port change.
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		if p, err := strconv.Atoi(port); err == nil {
			info.Port = p
		}
	}
	writeJSON(w, http.StatusOK, mocksResponse{Mocks: a.Mocks.All(), Info: info})
}

func (a *API) createMock(w http.ResponseWriter, r *http.Request) {
	var mock httpmock.Mock
	if !readJSON(w, r, &mock, true) {
		return
	}
	disabled, err := a.Mocks.Create(mock)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, disabledDuplicates{disabled})
}

// reloadMocks re-reads the mock files from disk.
func (a *API) reloadMocks(w http.ResponseWriter, r *http.Request) {
	if err := a.Mocks.Load(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *API) updateMock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var mock httpmock.Mock
	if !readJSON(w, r, &mock, true) {
		return
	}
	disabled, err := a.Mocks.Update(id, mock)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, disabledDuplicates{disabled})
}

func (a *API) deleteMock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Mocks.Delete(id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// toggleMock flips a mock between enabled and disabled.
func (a *API) toggleMock(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found, disabled := a.Mocks.Toggle(id)
	if !found {
		writeError(w, httpmock.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, disabledDuplicates{disabled})
}

// resetMockSequence moves a sequence mock back to its first step.
func (a *API) resetMockSequence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !a.Mocks.ResetSequence(id) {
		http.Error(w, "mock not found or not a sequence", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}
