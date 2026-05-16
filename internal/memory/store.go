package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	defectsFile    = "escaped-defects.jsonl"
	promotionsFile = "promoted-rules.jsonl"

	maxSummaryLen         = 4096
	maxRecommendedRuleLen = 2048
	maxRuleIDLen          = 256
	maxPromotionSummary   = 2048
)

// AppendLesson validates and appends an EscapedDefect record to the JSONL store.
// It rejects empty summary, unknown status, and unknown missed_by values.
func AppendLesson(dir string, lesson EscapedDefect) error {
	if lesson.Summary == "" {
		return fmt.Errorf("lesson summary must not be empty")
	}
	if len(lesson.Summary) > maxSummaryLen {
		return fmt.Errorf("lesson summary exceeds %d chars (got %d)", maxSummaryLen, len(lesson.Summary))
	}
	if len(lesson.RecommendedRule) > maxRecommendedRuleLen {
		return fmt.Errorf("lesson recommended_rule exceeds %d chars (got %d)", maxRecommendedRuleLen, len(lesson.RecommendedRule))
	}
	if !ValidStatus[lesson.Status] {
		return fmt.Errorf("unknown lesson status %q", lesson.Status)
	}
	for _, mb := range lesson.MissedBy {
		if !ValidMissedBy[mb] {
			return fmt.Errorf("unknown missed_by value %q", mb)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	path := filepath.Join(dir, defectsFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open defects file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(lesson)
	if err != nil {
		return fmt.Errorf("marshal lesson: %w", err)
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write lesson: %w", err)
	}
	return nil
}

// ListLessons reads all EscapedDefect records from the JSONL store.
// Deduplicates by ID using last-record-wins on highest CreatedAt.
// Returns in first-insertion order of unique IDs.
func ListLessons(dir string) ([]EscapedDefect, error) {
	path := filepath.Join(dir, defectsFile)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open defects file: %w", err)
	}
	defer f.Close()

	// Track order of first appearance and the best (newest CreatedAt) record per ID.
	order := []string{}
	seen := map[string]bool{}
	best := map[string]EscapedDefect{}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var lesson EscapedDefect
		if err := json.Unmarshal(line, &lesson); err != nil {
			return nil, fmt.Errorf("unmarshal lesson record: %w", err)
		}
		if !seen[lesson.ID] {
			seen[lesson.ID] = true
			order = append(order, lesson.ID)
			best[lesson.ID] = lesson
		} else {
			// Last-record-wins: replace if this record's CreatedAt is newer or equal.
			prev := best[lesson.ID]
			if !lesson.CreatedAt.Before(prev.CreatedAt) {
				best[lesson.ID] = lesson
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan defects file: %w", err)
	}

	out := make([]EscapedDefect, 0, len(order))
	for _, id := range order {
		out = append(out, best[id])
	}
	return out, nil
}

// GetLesson looks up a single EscapedDefect by ID. Returns false if not found.
func GetLesson(dir, id string) (EscapedDefect, bool, error) {
	lessons, err := ListLessons(dir)
	if err != nil {
		return EscapedDefect{}, false, err
	}
	for _, l := range lessons {
		if l.ID == id {
			return l, true, nil
		}
	}
	return EscapedDefect{}, false, nil
}

// AppendPromotion appends a PromotionEntry record to the promotions JSONL store.
func AppendPromotion(dir string, entry PromotionEntry) error {
	if len(entry.RuleID) > maxRuleIDLen {
		return fmt.Errorf("promotion rule_id exceeds %d chars (got %d)", maxRuleIDLen, len(entry.RuleID))
	}
	if len(entry.Summary) > maxPromotionSummary {
		return fmt.Errorf("promotion summary exceeds %d chars (got %d)", maxPromotionSummary, len(entry.Summary))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create memory dir: %w", err)
	}

	path := filepath.Join(dir, promotionsFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open promotions file: %w", err)
	}
	defer f.Close()

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal promotion entry: %w", err)
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write promotion entry: %w", err)
	}
	return nil
}

// ListPromotedRules reads all PromotionEntry records from the JSONL store.
func ListPromotedRules(dir string) ([]PromotionEntry, error) {
	path := filepath.Join(dir, promotionsFile)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open promotions file: %w", err)
	}
	defer f.Close()

	var entries []PromotionEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry PromotionEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("unmarshal promotion record: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan promotions file: %w", err)
	}
	return entries, nil
}
