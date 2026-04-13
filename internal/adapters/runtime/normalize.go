package runtime

import "strings"

// NormalizeKey lowercases and converts hyphens/spaces to underscores,
// producing a canonical key form for switch-based matching.
func NormalizeKey(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	replacer := strings.NewReplacer("-", "_", " ", "_")
	return replacer.Replace(raw)
}

// NormalizeWorkerStatusString maps raw status strings — including common
// synonyms and spelling variants (hyphenated, underscored, camelCase-collapsed)
// — to canonical WorkerStatus values.
//
// Returns the canonical status and true if the raw value was recognized,
// or ("", false) otherwise.
func NormalizeWorkerStatusString(raw string) (WorkerStatus, bool) {
	switch NormalizeKey(raw) {
	case "done", "completed", "complete", "success", "passed", "ok":
		return WorkerStatusDone, true
	case "done_with_concerns", "donewithconcerns", "concerns":
		return WorkerStatusDoneWithConcerns, true
	case "needs_context", "needscontext", "context_needed", "needs_more_context":
		return WorkerStatusNeedsContext, true
	case "blocked", "blocked_by_operator_input", "blockedbyoperatorinput":
		return WorkerStatusBlocked, true
	default:
		return "", false
	}
}
