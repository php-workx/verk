package constraints

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ConstraintIndex is the in-memory representation of .verk/constraints/index.json
type ConstraintIndex struct {
	SchemaVersion string       `json:"schema_version"`
	Constraints   []Constraint `json:"constraints"`
}

// Constraint is a single compiled constraint entry.
type Constraint struct {
	ID                  string         `json:"id"`
	CreatedAt           string         `json:"created_at"`
	FindingSignature    FindingSig     `json:"finding_signature"`
	PromotedFrom        []PromotedFrom `json:"promoted_from"`
	PromotedFromCount   int            `json:"promoted_from_count"`
	Check               CheckSpec      `json:"check"`
	Active              bool           `json:"active"`
	ActivationThreshold int            `json:"activation_threshold"`
	LastTriggeredAt     string         `json:"last_triggered_at,omitempty"`
	DisabledAt          string         `json:"disabled_at,omitempty"`
	DisabledReason      string         `json:"disabled_reason,omitempty"`
}

// FindingSig captures the pattern that defines a constraint's matching scope.
type FindingSig struct {
	TitleRegex     string `json:"title_regex"`
	FileGlob       string `json:"file_glob"`
	SeverityBucket string `json:"severity_bucket"`
}

// PromotedFrom records a finding occurrence that contributed to promotion.
type PromotedFrom struct {
	RunID     string `json:"run_id"`
	TicketID  string `json:"ticket_id"`
	FindingID string `json:"finding_id"`
}

// CheckSpec describes the deterministic check to run.
type CheckSpec struct {
	Type      string      `json:"type"` // "command" | "grep"
	Spec      interface{} `json:"spec"`
	TimeoutMs int         `json:"timeout_ms"`
}

// CommandSpec is the spec for check type "command".
type CommandSpec struct {
	Command  string   `json:"command"`
	Args     []string `json:"args"`
	CwdMode  string   `json:"cwd_mode"`  // "repo_root" | "worktree"
	BaseUsed string   `json:"base_used"` // "wave_base_commit" | "head"
}

// GrepSpec is the spec for check type "grep".
type GrepSpec struct {
	Pattern      string `json:"pattern"`
	FileGlob     string `json:"file_glob"`
	MustNotMatch bool   `json:"must_not_match"`
	BaseUsed     string `json:"base_used"`
}

// candidateEntry is one line in the candidate JSONL file.
type candidateEntry struct {
	RunID      string `json:"run_id"`
	TicketID   string `json:"ticket_id"`
	FindingID  string `json:"finding_id"`
	AppendedAt string `json:"appended_at"`
}

// Store manages the constraint index at .verk/constraints/index.json
// and candidate files at .verk/constraints/candidates/*.jsonl
type Store struct {
	indexPath     string
	candidatesDir string
}

// NewStore creates a Store rooted at repoRoot/.verk/constraints/.
func NewStore(repoRoot string) *Store {
	base := filepath.Join(repoRoot, ".verk", "constraints")
	return &Store{
		indexPath:     filepath.Join(base, "index.json"),
		candidatesDir: filepath.Join(base, "candidates"),
	}
}

// Load reads the index; returns empty index if file does not exist.
func (s *Store) Load() (*ConstraintIndex, error) {
	data, err := os.ReadFile(s.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &ConstraintIndex{SchemaVersion: "1", Constraints: nil}, nil
		}
		return nil, fmt.Errorf("read constraint index %q: %w", s.indexPath, err)
	}
	var idx ConstraintIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("unmarshal constraint index: %w", err)
	}
	return &idx, nil
}

// Save writes the index atomically.
func (s *Store) Save(idx *ConstraintIndex) error {
	if err := os.MkdirAll(filepath.Dir(s.indexPath), 0o755); err != nil {
		return fmt.Errorf("create constraint dir: %w", err)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal constraint index: %w", err)
	}
	data = append(data, '\n')
	return saveFileAtomic(s.indexPath, data)
}

// ListActiveConstraints returns all constraints with Active=true.
func (s *Store) ListActiveConstraints() ([]Constraint, error) {
	idx, err := s.Load()
	if err != nil {
		return nil, err
	}
	out := make([]Constraint, 0)
	for _, c := range idx.Constraints {
		if c.Active {
			out = append(out, c)
		}
	}
	return out, nil
}

// AppendCandidate records a finding occurrence for the given signature.
// Returns the distinct ticket count after appending.
func (s *Store) AppendCandidate(sig Signature, runID, ticketID, findingID string) (int, error) {
	if err := os.MkdirAll(s.candidatesDir, 0o755); err != nil {
		return 0, fmt.Errorf("create candidates dir: %w", err)
	}
	candPath := s.candidatePath(sig)
	entry := candidateEntry{
		RunID:      runID,
		TicketID:   ticketID,
		FindingID:  findingID,
		AppendedAt: time.Now().UTC().Format(time.RFC3339),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("marshal candidate entry: %w", err)
	}
	f, err := os.OpenFile(candPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("open candidate file %q: %w", candPath, err)
	}
	_, writeErr := fmt.Fprintf(f, "%s\n", line)
	closeErr := f.Close()
	if writeErr != nil {
		return 0, fmt.Errorf("write candidate entry: %w", writeErr)
	}
	if closeErr != nil {
		return 0, fmt.Errorf("close candidate file: %w", closeErr)
	}
	return s.DistinctTicketCount(sig)
}

// DistinctTicketCount counts distinct ticket_id entries in the candidate file.
func (s *Store) DistinctTicketCount(sig Signature) (int, error) {
	candPath := s.candidatePath(sig)
	f, err := os.Open(candPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("open candidate file %q: %w", candPath, err)
	}
	defer func() { _ = f.Close() }()

	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry candidateEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip malformed lines
		}
		if entry.TicketID != "" {
			seen[entry.TicketID] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan candidate file: %w", err)
	}
	return len(seen), nil
}

func (s *Store) candidatePath(sig Signature) string {
	return filepath.Join(s.candidatesDir, string(sig)+".jsonl")
}

// saveFileAtomic writes data to a temp file and renames atomically.
func saveFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}
