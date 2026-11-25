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
	DisabledDuplicates []int `json:"disabled_duplicates"`
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
	index, ok := mockIndex(w, r)
	if !ok {
		return
	}
	var mock httpmock.Mock
	if !readJSON(w, r, &mock, true) {
		return
	}
	disabled, err := a.Mocks.Update(index, mock)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, disabledDuplicates{disabled})
}

func (a *API) deleteMock(w http.ResponseWriter, r *http.Request) {
	index, ok := mockIndex(w, r)
	if !ok {
		return
	}
	if err := a.Mocks.Delete(index); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// toggleMock flips a mock between enabled and disabled.
func (a *API) toggleMock(w http.ResponseWriter, r *http.Request) {
	index, ok := mockIndex(w, r)
	if !ok {
		return
	}
	found, disabled := a.Mocks.Toggle(index)
	if !found {
		writeError(w, httpmock.ErrNotFound)
		return
	}
	writeJSON(w, http.StatusOK, disabledDuplicates{disabled})
}

// resetMockSequence moves a sequence mock back to its first step.
func (a *API) resetMockSequence(w http.ResponseWriter, r *http.Request) {
	index, ok := mockIndex(w, r)
	if !ok {
		return
	}
	if !a.Mocks.ResetSequence(index) {
		http.Error(w, "mock not found or not a sequence", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// mockIndex returns the {index} path value, answering 404 when it is not
// a number.
func mockIndex(w http.ResponseWriter, r *http.Request) (int, bool) {
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	return index, true
}
