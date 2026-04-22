package runtime

import "strings"

// NormalizeKey lowercases and converts hyphens/spaces to underscores,
// producing a canonical key form for map-based matching.
func NormalizeKey(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	replacer := strings.NewReplacer("-", "_", " ", "_")
	return replacer.Replace(raw)
}

var workerStatusVariants = map[string]WorkerStatus{
	// WorkerStatusDone
	"done":      WorkerStatusDone,
	"completed": WorkerStatusDone,
	"complete":  WorkerStatusDone,
	"success":   WorkerStatusDone,
	"passed":    WorkerStatusDone,
	"ok":        WorkerStatusDone,
	// WorkerStatusDoneWithConcerns
	"done_with_concerns": WorkerStatusDoneWithConcerns,
	"donewithconcerns":   WorkerStatusDoneWithConcerns,
	"concerns":           WorkerStatusDoneWithConcerns,
	// WorkerStatusNeedsContext
	"needs_context":      WorkerStatusNeedsContext,
	"needscontext":       WorkerStatusNeedsContext,
	"context_needed":     WorkerStatusNeedsContext,
	"needs_more_context": WorkerStatusNeedsContext,
	"needsmorecontext":   WorkerStatusNeedsContext,
	// WorkerStatusBlocked
	"blocked":                   WorkerStatusBlocked,
	"blocked_by_operator_input": WorkerStatusBlocked,
	"blockedbyoperatorinput":    WorkerStatusBlocked,
}

// NormalizeWorkerStatusString maps raw status strings — including common
// synonyms and spelling variants (hyphenated, underscored, camelCase-collapsed)
// — to canonical WorkerStatus values.
//
// Returns the canonical status and true if the raw value was recognized,
// or ("", false) otherwise.
func NormalizeWorkerStatusString(raw string) (WorkerStatus, bool) {
	status, ok := workerStatusVariants[NormalizeKey(raw)]
	return status, ok
}
