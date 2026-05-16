package memory

import (
	"fmt"
	"testing"
	"time"
)

func TestAppendLesson_PersistsValidLesson(t *testing.T) {
	dir := t.TempDir()
	lesson := EscapedDefect{
		ID:        fmt.Sprintf("learn-%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC(),
		Summary:   "test summary",
		MissedBy:  []string{"reviewer"},
		Status:    StatusProposed,
	}
	if err := AppendLesson(dir, lesson); err != nil {
		t.Fatalf("AppendLesson: %v", err)
	}
	lessons, err := ListLessons(dir)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(lessons) != 1 {
		t.Fatalf("expected 1 lesson, got %d", len(lessons))
	}
	if lessons[0].ID != lesson.ID {
		t.Errorf("lesson ID = %q, want %q", lessons[0].ID, lesson.ID)
	}
	if lessons[0].Summary != lesson.Summary {
		t.Errorf("lesson Summary = %q, want %q", lessons[0].Summary, lesson.Summary)
	}
}

func TestAppendLesson_RejectsEmptySummary(t *testing.T) {
	dir := t.TempDir()
	lesson := EscapedDefect{
		ID:        fmt.Sprintf("learn-%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC(),
		Summary:   "",
		Status:    StatusProposed,
	}
	if err := AppendLesson(dir, lesson); err == nil {
		t.Fatal("expected error for empty summary, got nil")
	}
}

func TestAppendLesson_RejectsUnknownMissedBy(t *testing.T) {
	dir := t.TempDir()
	lesson := EscapedDefect{
		ID:        fmt.Sprintf("learn-%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC(),
		Summary:   "test summary",
		MissedBy:  []string{"nonsense"},
		Status:    StatusProposed,
	}
	if err := AppendLesson(dir, lesson); err == nil {
		t.Fatal("expected error for unknown missed_by, got nil")
	}
}

func TestAppendLesson_RejectsUnknownStatus(t *testing.T) {
	dir := t.TempDir()
	lesson := EscapedDefect{
		ID:        fmt.Sprintf("learn-%d", time.Now().UnixNano()),
		CreatedAt: time.Now().UTC(),
		Summary:   "test summary",
		Status:    EscapedDefectStatus("invalid-status"),
	}
	if err := AppendLesson(dir, lesson); err == nil {
		t.Fatal("expected error for unknown status, got nil")
	}
}

func TestListLessons_ReturnsFirstInsertionOrder(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	ids := []string{
		fmt.Sprintf("learn-%d", now.UnixNano()),
		fmt.Sprintf("learn-%d", now.Add(time.Millisecond).UnixNano()),
		fmt.Sprintf("learn-%d", now.Add(2*time.Millisecond).UnixNano()),
	}
	for i, id := range ids {
		lesson := EscapedDefect{
			ID:        id,
			CreatedAt: now.Add(time.Duration(i) * time.Millisecond),
			Summary:   fmt.Sprintf("summary %d", i),
			Status:    StatusProposed,
		}
		if err := AppendLesson(dir, lesson); err != nil {
			t.Fatalf("AppendLesson %d: %v", i, err)
		}
	}
	lessons, err := ListLessons(dir)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	if len(lessons) != 3 {
		t.Fatalf("expected 3 lessons, got %d", len(lessons))
	}
	for i, l := range lessons {
		if l.ID != ids[i] {
			t.Errorf("position %d: ID = %q, want %q", i, l.ID, ids[i])
		}
	}
}

func TestListLessons_DeduplicatesByIDLastRecordWins(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	id := fmt.Sprintf("learn-%d", now.UnixNano())

	// Append original record.
	original := EscapedDefect{
		ID:        id,
		CreatedAt: now,
		Summary:   "original summary",
		Status:    StatusProposed,
	}
	if err := AppendLesson(dir, original); err != nil {
		t.Fatalf("AppendLesson original: %v", err)
	}

	// Append a second record with same ID but newer CreatedAt and updated status.
	updated := EscapedDefect{
		ID:        id,
		CreatedAt: now.Add(time.Second),
		Summary:   "updated summary",
		Status:    StatusPromoted,
	}
	if err := AppendLesson(dir, updated); err != nil {
		t.Fatalf("AppendLesson updated: %v", err)
	}

	lessons, err := ListLessons(dir)
	if err != nil {
		t.Fatalf("ListLessons: %v", err)
	}
	// Should deduplicate to 1 entry.
	if len(lessons) != 1 {
		t.Fatalf("expected 1 lesson after dedup, got %d", len(lessons))
	}
	// Last-record-wins: updated record should be returned.
	if lessons[0].Status != StatusPromoted {
		t.Errorf("expected status %q, got %q", StatusPromoted, lessons[0].Status)
	}
	if lessons[0].Summary != "updated summary" {
		t.Errorf("expected updated summary, got %q", lessons[0].Summary)
	}
	// Ordering preserves first appearance (position 0).
}

func TestAppendPromotion_PersistsEntry(t *testing.T) {
	dir := t.TempDir()
	entry := PromotionEntry{
		LessonID:   "learn-123",
		PromotedAt: time.Now().UTC(),
		Target:     ".agents/patterns/my-pattern.md",
		RuleID:     "rule-001",
		Summary:    "do not ignore errors",
	}
	if err := AppendPromotion(dir, entry); err != nil {
		t.Fatalf("AppendPromotion: %v", err)
	}
	entries, err := ListPromotedRules(dir)
	if err != nil {
		t.Fatalf("ListPromotedRules: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].LessonID != entry.LessonID {
		t.Errorf("LessonID = %q, want %q", entries[0].LessonID, entry.LessonID)
	}
	if entries[0].RuleID != entry.RuleID {
		t.Errorf("RuleID = %q, want %q", entries[0].RuleID, entry.RuleID)
	}
}

func TestListPromotedRules_ReturnsAllEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		entry := PromotionEntry{
			LessonID:   fmt.Sprintf("learn-%d", i),
			PromotedAt: now.Add(time.Duration(i) * time.Second),
			Target:     ".agents/patterns/pattern.md",
			RuleID:     fmt.Sprintf("rule-%03d", i),
		}
		if err := AppendPromotion(dir, entry); err != nil {
			t.Fatalf("AppendPromotion %d: %v", i, err)
		}
	}
	entries, err := ListPromotedRules(dir)
	if err != nil {
		t.Fatalf("ListPromotedRules: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}
