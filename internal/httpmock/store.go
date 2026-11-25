package httpmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ditto/internal/jsonfile"
)

// ErrNotFound is returned for a mock index outside the store.
var ErrNotFound = errors.New("mock not found")

// Store holds the mocks loaded from one directory, one JSON file per mock,
// and keeps each sequence mock's position in memory. Mocks are addressed by
// their index in All.
type Store struct {
	mu    sync.RWMutex
	mocks []Mock
	dir   string
}

// NewStore returns an empty store for dir; call Load to read it.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// Load replaces the store's mocks with the files in dir. Every loaded mock
// starts enabled.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mocks, err := loadMocksFromDir(s.dir)
	if err != nil {
		return err
	}
	s.mocks = mocks
	return nil
}

// findBestLocked returns the index of the best matching enabled mock, or -1.
// Caller must hold s.mu (read or write).
func (s *Store) findBestLocked(r *http.Request, reqBody []byte) int {
	bestIdx := -1
	bestScore := -1
	bestPathScore := -1
	for i := range s.mocks {
		m := &s.mocks[i]
		if !m.Enabled {
			continue
		}
		if !strings.EqualFold(m.Method, r.Method) || !matchPath(m.Path, r.URL.Path) {
			continue
		}
		if !matchConditions(m.Match, r, reqBody) {
			continue
		}
		score := m.Match.Specificity()
		pathScore := pathSpecificity(m.Path)
		if score > bestScore || (score == bestScore && pathScore > bestPathScore) {
			bestIdx = i
			bestScore = score
			bestPathScore = pathScore
		}
	}
	return bestIdx
}

// Resolved is a snapshot of what to send for one matched request.
type Resolved struct {
	Status    int
	Headers   map[string]string
	Body      []byte
	DelayMs   int
	MockIndex int
	// Sequence-only fields; zero-valued when not a sequence response.
	IsSequence   bool
	SequenceStep int // 1-based step that was served; 0 for reset-fallback
	SequenceLen  int
}

// Resolve finds the best matching mock and returns a response snapshot.
// For sequence mocks, it advances the in-memory counter atomically.
// Returns nil if no mock matches.
func (s *Store) Resolve(r *http.Request, reqBody []byte) *Resolved {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findBestLocked(r, reqBody)
	if idx < 0 {
		return nil
	}
	m := &s.mocks[idx]

	if !m.IsSequence() {
		return &Resolved{
			Status:    m.Status,
			Headers:   m.Headers,
			Body:      m.RawBody,
			DelayMs:   m.DelayMs,
			MockIndex: idx,
		}
	}

	seq := m.Sequence
	n := len(seq.Steps)
	cur := seq.CurrentStep

	// On reset mode, once we've exhausted all steps, serve the static body
	// once and then start over from step 0.
	if seq.OnEnd == "reset" && cur >= n {
		seq.CurrentStep = 0
		return &Resolved{
			Status:       m.Status,
			Headers:      m.Headers,
			Body:         m.RawBody,
			DelayMs:      m.DelayMs,
			MockIndex:    idx,
			IsSequence:   true,
			SequenceStep: 0, // fall-back call between cycles
			SequenceLen:  n,
		}
	}

	// On proxy mode, once we've exhausted all steps, stop resolving this mock
	// so the request can fall through to the real backend target.
	if seq.OnEnd == "proxy" && cur >= n {
		return nil
	}

	// Clamp in case state is out of range (e.g., steps shrank since last call).
	if cur < 0 || cur >= n {
		cur = 0
		seq.CurrentStep = 0
	}

	step := seq.Steps[cur]
	served := cur + 1 // 1-based for logging

	next := cur + 1
	switch seq.OnEnd {
	case "stay":
		if next >= n {
			next = n - 1
		}
	case "reset", "proxy":
		// Let next reach n; the following call serves the static fallback or
		// falls through to the real backend, depending on the mode.
	default: // "loop" and unknown modes
		if next >= n {
			next = 0
		}
	}
	seq.CurrentStep = next

	body := step.Body
	if len(body) == 0 {
		body = []byte("null")
	}
	status := step.Status
	if status == 0 {
		status = m.Status
	}

	return &Resolved{
		Status:       status,
		Headers:      step.Headers,
		Body:         body,
		DelayMs:      step.DelayMs,
		MockIndex:    idx,
		IsSequence:   true,
		SequenceStep: served,
		SequenceLen:  n,
	}
}

// ResetSequence resets the in-memory counter of the sequence at the given index.
// Returns false if the index is invalid or the mock is not a sequence.
func (s *Store) ResetSequence(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= len(s.mocks) {
		return false
	}
	m := &s.mocks[index]
	if m.Sequence == nil {
		return false
	}
	m.Sequence.CurrentStep = 0
	return true
}

// All returns a copy of every mock in index order.
func (s *Store) All() []Mock {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Mock, len(s.mocks))
	copy(result, s.mocks)
	return result
}

// Toggle flips a mock's enabled state. When enabling, any other mock with
// identical method + path + match conditions is auto-disabled to prevent
// ambiguity. Returns the indices of any mocks that were auto-disabled.
func (s *Store) Toggle(index int) (bool, []int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= len(s.mocks) {
		return false, nil
	}
	s.mocks[index].Enabled = !s.mocks[index].Enabled

	var disabled []int
	if s.mocks[index].Enabled {
		disabled = s.disableDuplicatesLocked(index)
	}
	return true, disabled
}

// Count returns the number of mocks.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.mocks)
}

// Create validates, enables and saves a new mock, returning the indices of
// mocks it disabled because they had the same method, path and conditions.
func (s *Store) Create(mock Mock) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if mock.Method == "" || mock.Path == "" {
		return nil, fmt.Errorf("method and path are required")
	}
	if mock.Status == 0 {
		mock.Status = 200
	}
	mock.RawBody = []byte(mock.Body)
	mock.Enabled = true

	if mock.Source == "" {
		mock.Source = generateFilename(mock.Method, mock.Path, s.mocks)
	}

	if err := writeMockFile(s.dir, mock); err != nil {
		return nil, err
	}

	s.mocks = append(s.mocks, mock)
	disabled := s.disableDuplicatesLocked(len(s.mocks) - 1)
	return disabled, nil
}

// Update replaces the mock at index, keeping its enabled state and sequence
// position, and returns the indices of duplicates it disabled.
func (s *Store) Update(index int, mock Mock) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= len(s.mocks) {
		return nil, ErrNotFound
	}
	if mock.Method == "" || mock.Path == "" {
		return nil, fmt.Errorf("method and path are required")
	}
	if mock.Status == 0 {
		mock.Status = 200
	}
	mock.RawBody = []byte(mock.Body)
	mock.Enabled = s.mocks[index].Enabled

	// Preserve in-memory sequence cursor across edits so callers aren't
	// surprised by a reset mid-flow. If the step count shrank below the
	// saved cursor, Resolve clamps it on the next call.
	if mock.Sequence != nil && s.mocks[index].Sequence != nil {
		mock.Sequence.CurrentStep = s.mocks[index].Sequence.CurrentStep
	}

	oldSource := s.mocks[index].Source
	if mock.Source == "" {
		mock.Source = oldSource
	}

	if mock.Source != oldSource {
		os.Remove(filepath.Join(s.dir, oldSource))
	}

	if err := writeMockFile(s.dir, mock); err != nil {
		return nil, err
	}

	s.mocks[index] = mock

	var disabled []int
	if mock.Enabled {
		disabled = s.disableDuplicatesLocked(index)
	}
	return disabled, nil
}

// Delete removes the mock at index and its file.
func (s *Store) Delete(index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= len(s.mocks) {
		return ErrNotFound
	}

	filePath := filepath.Join(s.dir, s.mocks[index].Source)
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting %s: %w", filePath, err)
	}

	s.mocks = append(s.mocks[:index], s.mocks[index+1:]...)
	return nil
}

// disableDuplicatesLocked auto-disables any other enabled mocks that share
// method + path + match conditions with the mock at the given index.
// MUST be called with s.mu held.
func (s *Store) disableDuplicatesLocked(activeIndex int) []int {
	active := s.mocks[activeIndex]
	if !active.Enabled {
		return nil
	}

	var disabled []int
	for i := range s.mocks {
		if i == activeIndex || !s.mocks[i].Enabled {
			continue
		}
		other := s.mocks[i]
		if !strings.EqualFold(other.Method, active.Method) {
			continue
		}
		if other.Path != active.Path {
			continue
		}
		if !other.Match.Equals(active.Match) {
			continue
		}
		s.mocks[i].Enabled = false
		disabled = append(disabled, i)
	}
	return disabled
}

func generateFilename(method, path string, existing []Mock) string {
	clean := strings.ReplaceAll(path, "/", "_")
	clean = strings.TrimPrefix(clean, "_")
	if clean == "" {
		clean = "root"
	}
	base := fmt.Sprintf("%s_%s", strings.ToLower(method), clean)
	name := base + ".json"

	taken := make(map[string]bool, len(existing))
	for _, m := range existing {
		taken[m.Source] = true
	}

	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s_%d.json", base, i)
	}
	return name
}

func writeMockFile(dir string, mock Mock) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating mocks dir: %w", err)
	}

	fileData := struct {
		Method       string            `json:"method"`
		Path         string            `json:"path"`
		Status       int               `json:"status"`
		Headers      map[string]string `json:"headers,omitempty"`
		Body         json.RawMessage   `json:"body"`
		DelayMs      int               `json:"delay_ms,omitempty"`
		Match        *Match            `json:"match,omitempty"`
		ResponseMode string            `json:"response_mode,omitempty"`
		Sequence     *Sequence         `json:"sequence,omitempty"`
	}{
		Method:       mock.Method,
		Path:         mock.Path,
		Status:       mock.Status,
		Headers:      mock.Headers,
		Body:         mock.Body,
		DelayMs:      mock.DelayMs,
		ResponseMode: mock.ResponseMode,
	}
	if mock.Sequence != nil {
		seqCopy := *mock.Sequence
		seqCopy.CurrentStep = 0 // never persist in-memory cursor to disk
		fileData.Sequence = &seqCopy
	}
	if !mock.Match.IsEmpty() {
		fileData.Match = &mock.Match
	}

	data, err := json.MarshalIndent(fileData, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling mock: %w", err)
	}

	filePath := filepath.Join(dir, mock.Source)
	return jsonfile.WriteAtomic(filePath, data, 0o644)
}

func loadMocksFromDir(dir string) ([]Mock, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}

	var mocks []Mock
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", file, err)
		}

		var mock Mock
		if err := json.Unmarshal(data, &mock); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", file, err)
		}

		if mock.Method == "" || mock.Path == "" {
			return nil, fmt.Errorf("%s: method and path are required", file)
		}
		if mock.Status == 0 {
			mock.Status = 200
		}

		mock.RawBody = []byte(mock.Body)
		mock.Enabled = true
		mock.Source = filepath.Base(file)
		mocks = append(mocks, mock)
	}

	return mocks, nil
}
