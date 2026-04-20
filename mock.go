package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

// Match holds optional conditions a request must satisfy beyond method + path.
// All fields are optional; a request matches if every specified condition is met.
type Match struct {
	Query   map[string]string `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// IsEmpty reports whether no conditions are defined.
func (m Match) IsEmpty() bool {
	return len(m.Query) == 0 && len(m.Headers) == 0 && len(m.Body) == 0
}

// Specificity returns the number of conditions defined; used to prefer the
// most specific mock when multiple mocks could match the same request.
func (m Match) Specificity() int {
	score := len(m.Query) + len(m.Headers)
	if len(m.Body) > 0 {
		score++
	}
	return score
}

// Equals reports whether two Match definitions describe the same conditions.
func (m Match) Equals(other Match) bool {
	if !reflect.DeepEqual(m.Query, other.Query) {
		return false
	}
	if !equalCaseInsensitive(m.Headers, other.Headers) {
		return false
	}
	return jsonEqual(m.Body, other.Body)
}

type Mock struct {
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Status       int               `json:"status"`
	Headers      map[string]string `json:"headers,omitempty"`
	Body         json.RawMessage   `json:"body"`
	RawBody      []byte            `json:"-"`
	DelayMs      int               `json:"delay_ms,omitempty"`
	Match        Match             `json:"match,omitempty"`
	Enabled      bool              `json:"enabled"`
	Source       string            `json:"source"`
	ResponseMode string            `json:"response_mode,omitempty"` // "static" (default) | "sequence"
	Sequence     *Sequence         `json:"sequence,omitempty"`
}

// Sequence lets a single mock return different responses on subsequent calls.
// CurrentStep is in-memory only (not persisted to disk); it resets when the
// server restarts. It is exposed over the wire so the UI can render the
// current position, but writeMockFile uses its own struct that excludes it.
type Sequence struct {
	Steps       []SequenceStep `json:"steps"`
	OnEnd       string         `json:"on_end"` // "loop" | "stay" | "reset"
	CurrentStep int            `json:"current_step,omitempty"`
}

type SequenceStep struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body"`
	DelayMs int               `json:"delay_ms,omitempty"`
}

// IsSequence reports whether this mock should serve a sequence response.
func (m *Mock) IsSequence() bool {
	return m.ResponseMode == "sequence" && m.Sequence != nil && len(m.Sequence.Steps) > 0
}

type MockStore struct {
	mu    sync.RWMutex
	mocks []Mock
	dir   string
}

func NewMockStore(dir string) *MockStore {
	return &MockStore{dir: dir}
}

func (s *MockStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	mocks, err := loadMocksFromDir(s.dir)
	if err != nil {
		return err
	}
	s.mocks = mocks
	return nil
}

// Find returns the most specific enabled mock that matches the request.
// reqBody may be nil if the request has no body to inspect.
func (s *MockStore) Find(r *http.Request, reqBody []byte) *Mock {
	s.mu.RLock()
	defer s.mu.RUnlock()

	idx := s.findBestLocked(r, reqBody)
	if idx < 0 {
		return nil
	}
	return &s.mocks[idx]
}

// findBestLocked returns the index of the best matching enabled mock, or -1.
// Caller must hold s.mu (read or write).
func (s *MockStore) findBestLocked(r *http.Request, reqBody []byte) int {
	bestIdx := -1
	bestScore := -1
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
		if score > bestScore {
			bestIdx = i
			bestScore = score
		}
	}
	return bestIdx
}

// ResolvedResponse is a snapshot of what to send for one matched request.
type ResolvedResponse struct {
	Status    int
	Headers   map[string]string
	Body      []byte
	DelayMs   int
	MockIndex int
	// Sequence-only fields; zero-valued when not a sequence response.
	IsSequence   bool
	SequenceStep int // 1-based step that was served
	SequenceLen  int
	SequenceEnd  string // "loop" | "stay" | "reset"
}

// MatchAndResolve finds the best matching mock and returns a response snapshot.
// For sequence mocks, it advances the in-memory counter atomically.
// Returns nil if no mock matches.
func (s *MockStore) MatchAndResolve(r *http.Request, reqBody []byte) *ResolvedResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.findBestLocked(r, reqBody)
	if idx < 0 {
		return nil
	}
	m := &s.mocks[idx]

	if !m.IsSequence() {
		return &ResolvedResponse{
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
		return &ResolvedResponse{
			Status:       m.Status,
			Headers:      m.Headers,
			Body:         m.RawBody,
			DelayMs:      m.DelayMs,
			MockIndex:    idx,
			IsSequence:   true,
			SequenceStep: 0, // indicates the fall-back call
			SequenceLen:  n,
			SequenceEnd:  seq.OnEnd,
		}
	}

	// Clamp in case state is out of range (e.g., steps shrank since last call).
	if cur < 0 || cur >= n {
		cur = 0
		seq.CurrentStep = 0
	}

	step := seq.Steps[cur]
	served := cur + 1 // 1-based

	// Advance counter for next call.
	next := cur + 1
	switch seq.OnEnd {
	case "loop":
		if next >= n {
			next = 0
		}
	case "stay":
		if next >= n {
			next = n - 1
		}
	case "reset":
		// Let it go to n; the next call will serve the static body and reset.
	default:
		// Unknown mode — default to loop behavior.
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

	return &ResolvedResponse{
		Status:       status,
		Headers:      step.Headers,
		Body:         body,
		DelayMs:      step.DelayMs,
		MockIndex:    idx,
		IsSequence:   true,
		SequenceStep: served,
		SequenceLen:  n,
		SequenceEnd:  seq.OnEnd,
	}
}

// ResetSequence resets the in-memory counter of the sequence at the given index.
// Returns false if the index is invalid or the mock is not a sequence.
func (s *MockStore) ResetSequence(index int) bool {
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

func (s *MockStore) All() []Mock {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Mock, len(s.mocks))
	copy(result, s.mocks)
	return result
}

// Toggle flips a mock's enabled state. When enabling, any other mock with
// identical method + path + match conditions is auto-disabled to prevent
// ambiguity. Returns the indices of any mocks that were auto-disabled.
func (s *MockStore) Toggle(index int) (bool, []int) {
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

func (s *MockStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.mocks)
}

func (s *MockStore) Create(mock Mock) ([]int, error) {
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

func (s *MockStore) Update(index int, mock Mock) ([]int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= len(s.mocks) {
		return nil, fmt.Errorf("mock not found")
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
	// saved cursor, MatchAndResolve clamps it on the next call.
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

func (s *MockStore) Delete(index int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if index < 0 || index >= len(s.mocks) {
		return fmt.Errorf("mock not found")
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
func (s *MockStore) disableDuplicatesLocked(activeIndex int) []int {
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
	return os.WriteFile(filePath, data, 0o644)
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

// matchPath supports exact matches and simple wildcards.
// Example: /api/v1/users/* matches /api/v1/users/123
func matchPath(pattern, path string) bool {
	if pattern == path {
		return true
	}

	patternParts := strings.Split(pattern, "/")
	pathParts := strings.Split(path, "/")

	if len(patternParts) != len(pathParts) {
		return false
	}

	for i := range patternParts {
		if patternParts[i] == "*" {
			continue
		}
		if patternParts[i] != pathParts[i] {
			return false
		}
	}

	return true
}

// matchConditions checks whether the request satisfies the given Match block.
// An empty Match always matches.
func matchConditions(m Match, r *http.Request, reqBody []byte) bool {
	if m.IsEmpty() {
		return true
	}

	if !matchQuery(m.Query, r.URL.Query()) {
		return false
	}
	if !matchHeaders(m.Headers, r.Header) {
		return false
	}
	if len(m.Body) > 0 && !matchBody(m.Body, reqBody) {
		return false
	}
	return true
}

func matchQuery(expected map[string]string, actual url.Values) bool {
	for k, v := range expected {
		if actual.Get(k) != v {
			return false
		}
	}
	return true
}

func matchHeaders(expected map[string]string, actual http.Header) bool {
	for k, v := range expected {
		if actual.Get(k) != v {
			return false
		}
	}
	return true
}

// matchBody returns true when every field in expected is present in actual
// with the same value (partial / subset match).
func matchBody(expected, actual []byte) bool {
	if len(actual) == 0 {
		return false
	}
	var exp, got any
	if err := json.Unmarshal(expected, &exp); err != nil {
		return false
	}
	if err := json.Unmarshal(actual, &got); err != nil {
		return false
	}
	return jsonSubset(exp, got)
}

// jsonSubset reports whether expected is a subset of actual.
// Maps are compared field-by-field; non-map values must be deeply equal.
func jsonSubset(expected, actual any) bool {
	expMap, expIsMap := expected.(map[string]any)
	gotMap, gotIsMap := actual.(map[string]any)

	if expIsMap && gotIsMap {
		for k, v := range expMap {
			gv, ok := gotMap[k]
			if !ok || !jsonSubset(v, gv) {
				return false
			}
		}
		return true
	}

	return reflect.DeepEqual(expected, actual)
}

func equalCaseInsensitive(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	lowerA := make(map[string]string, len(a))
	lowerB := make(map[string]string, len(b))
	for k, v := range a {
		lowerA[strings.ToLower(k)] = v
	}
	for k, v := range b {
		lowerB[strings.ToLower(k)] = v
	}
	return reflect.DeepEqual(lowerA, lowerB)
}

func jsonEqual(a, b json.RawMessage) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	var av, bv any
	if err := json.Unmarshal(a, &av); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}
