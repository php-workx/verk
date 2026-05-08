package epos

import (
	"fmt"
	"reflect"

	eposticket "github.com/php-workx/epos/ticket"
	"gopkg.in/yaml.v3"
)

func toEpos(t Ticket) *eposticket.Ticket { //nolint:cyclop // table-style bridge for epos named frontmatter fields
	out := &eposticket.Ticket{
		ID:                 t.ID,
		Title:              t.Title,
		Status:             eposticket.Status(t.Status),
		Deps:               append([]string(nil), t.Deps...),
		Priority:           t.Priority,
		AcceptanceCriteria: append([]string(nil), t.AcceptanceCriteria...),
		TestCases:          append([]string(nil), t.TestCases...),
		ValidationCommands: append([]string(nil), t.ValidationCommands...),
		Scope: eposticket.TaskScope{
			OwnedPaths: append([]string(nil), t.OwnedPaths...),
		},
		ReviewThreshold:   t.ReviewThreshold,
		RuntimePreference: t.Runtime,
		Extra:             map[string]any{},
		Present:           cloneBoolMap(t.present),
		TitleDerived:      t.titleDerived,
	}
	if out.Present == nil {
		out.Present = map[string]bool{}
	}
	markPresentForValues(out, t)
	if t.Model != "" {
		out.Extra["model"] = t.Model
		out.Present["model"] = true
	}
	for key, value := range t.UnknownFrontmatter {
		switch key {
		case "parent":
			out.Parent = asString(value)
		case "type":
			out.Type = asString(value)
		case "extended_status":
			out.ExtendedStatus = asString(value)
		case "created":
			out.Created = asString(value)
		case "updated_at":
			out.UpdatedAt = asString(value)
		case "order":
			out.Order = asInt(value)
		case "tags":
			out.Tags = asStringSlice(value)
		case "description":
			out.Description = asString(value)
		case "notes":
			out.Notes = asStringSlice(value)
		case "requirement_ids":
			out.RequirementIDs = asStringSlice(value)
		case "source_refs":
			out.SourceRefs = asStringSlice(value)
		case "lineage_id":
			out.LineageID = asString(value)
		case "risk_level":
			out.RiskLevel = asString(value)
		case "intent":
			out.Intent = asString(value)
		case "constraints":
			out.Constraints = asStringSlice(value)
		case "warnings":
			out.Warnings = asStringSlice(value)
		case "read_only_paths":
			out.Scope.ReadOnlyPaths = asStringSlice(value)
		case "shared_paths":
			out.Scope.SharedPaths = asStringSlice(value)
		case "isolation_mode":
			out.Scope.IsolationMode = asString(value)
		case "files_likely_touched":
			out.FilesLikelyTouched = asStringSlice(value)
		case "implementation_detail":
			out.ImplementationDetail = asTyped[eposticket.ImplementationDetail](value)
		case "learning_context":
			out.LearningContext = asTypedSlice[eposticket.LearningRef](value)
		case "validation_checks":
			out.ValidationChecks = asTypedSlice[eposticket.ValidationCheck](value)
		case "required_evidence":
			out.RequiredEvidence = asStringSlice(value)
		case "reviewer_guidance":
			out.ReviewerGuidance = asString(value)
		case "assignee":
			out.Assignee = asString(value)
		case "status_reason":
			out.StatusReason = asString(value)
		case "links":
			out.Links = asStringSlice(value)
		case "etag":
			out.ETag = asString(value)
		case "created_from":
			out.CreatedFrom = asString(value)
		case "grouping_reason":
			out.GroupingReason = asString(value)
		case "grouped_requirement_ids":
			out.GroupedRequirementIDs = asStringSlice(value)
		case "model":
			if t.Model == "" {
				out.Extra["model"] = value
			}
		default:
			out.Extra[key] = value
		}
		out.Present[key] = true
	}
	if len(out.Extra) == 0 {
		out.Extra = nil
	}
	return out
}

func fromEpos(t *eposticket.Ticket) Ticket {
	if t == nil {
		return Ticket{UnknownFrontmatter: map[string]any{}, present: map[string]bool{}}
	}
	unknown := map[string]any{}
	copyNamedString(unknown, "parent", t.Parent, t.Present)
	copyNamedString(unknown, "type", t.Type, t.Present)
	copyNamedString(unknown, "extended_status", t.ExtendedStatus, t.Present)
	copyNamedString(unknown, "created", t.Created, t.Present)
	copyNamedString(unknown, "updated_at", t.UpdatedAt, t.Present)
	copyNamedInt(unknown, "order", t.Order, t.Present)
	copyNamedStringSlice(unknown, "tags", t.Tags, t.Present)
	copyNamedString(unknown, "description", t.Description, t.Present)
	copyNamedStringSlice(unknown, "notes", t.Notes, t.Present)
	copyNamedStringSlice(unknown, "requirement_ids", t.RequirementIDs, t.Present)
	copyNamedStringSlice(unknown, "source_refs", t.SourceRefs, t.Present)
	copyNamedString(unknown, "lineage_id", t.LineageID, t.Present)
	copyNamedString(unknown, "risk_level", t.RiskLevel, t.Present)
	copyNamedString(unknown, "intent", t.Intent, t.Present)
	copyNamedStringSlice(unknown, "constraints", t.Constraints, t.Present)
	copyNamedStringSlice(unknown, "warnings", t.Warnings, t.Present)
	copyNamedStringSlice(unknown, "read_only_paths", t.Scope.ReadOnlyPaths, t.Present)
	copyNamedStringSlice(unknown, "shared_paths", t.Scope.SharedPaths, t.Present)
	copyNamedString(unknown, "isolation_mode", t.Scope.IsolationMode, t.Present)
	copyNamedStringSlice(unknown, "files_likely_touched", t.FilesLikelyTouched, t.Present)
	copyNamedStruct(unknown, "implementation_detail", t.ImplementationDetail, t.Present)
	copyNamedSlice(unknown, "learning_context", t.LearningContext, t.Present)
	copyNamedSlice(unknown, "validation_checks", t.ValidationChecks, t.Present)
	copyNamedStringSlice(unknown, "required_evidence", t.RequiredEvidence, t.Present)
	copyNamedString(unknown, "reviewer_guidance", t.ReviewerGuidance, t.Present)
	copyNamedString(unknown, "assignee", t.Assignee, t.Present)
	copyNamedString(unknown, "status_reason", t.StatusReason, t.Present)
	copyNamedStringSlice(unknown, "links", t.Links, t.Present)
	copyNamedString(unknown, "etag", t.ETag, t.Present)
	copyNamedString(unknown, "created_from", t.CreatedFrom, t.Present)
	copyNamedString(unknown, "grouping_reason", t.GroupingReason, t.Present)
	copyNamedStringSlice(unknown, "grouped_requirement_ids", t.GroupedRequirementIDs, t.Present)

	model := ""
	for key, value := range t.Extra {
		if key == "model" {
			model = asString(value)
			unknown[key] = value
			continue
		}
		unknown[key] = value
	}
	if t.ExtendedStatus == "" && isExtendedStatus(t.Status) {
		unknown["extended_status"] = string(t.Status)
	}
	return Ticket{
		ID:                 t.ID,
		Title:              t.Title,
		Status:             normalizeStatus(t.Status),
		Deps:               append([]string(nil), t.Deps...),
		Priority:           t.Priority,
		AcceptanceCriteria: append([]string(nil), t.AcceptanceCriteria...),
		TestCases:          append([]string(nil), t.TestCases...),
		ValidationCommands: append([]string(nil), t.ValidationCommands...),
		OwnedPaths:         append([]string(nil), t.Scope.OwnedPaths...),
		ReviewThreshold:    t.ReviewThreshold,
		Runtime:            t.RuntimePreference,
		Model:              model,
		UnknownFrontmatter: unknown,
		present:            cloneBoolMap(t.Present),
		titleDerived:       t.TitleDerived,
	}
}

func markPresentForValues(out *eposticket.Ticket, in Ticket) {
	markStringPresent(out.Present, "id", out.ID)
	if !in.titleDerived {
		markStringPresent(out.Present, "title", out.Title)
	}
	markStringPresent(out.Present, "status", string(out.Status))
	markStringSlicePresent(out.Present, "deps", out.Deps)
	if out.Priority != 0 {
		out.Present["priority"] = true
	}
	markStringSlicePresent(out.Present, "acceptance_criteria", out.AcceptanceCriteria)
	markStringSlicePresent(out.Present, "test_cases", out.TestCases)
	markStringSlicePresent(out.Present, "validation_commands", out.ValidationCommands)
	markStringSlicePresent(out.Present, "owned_paths", out.Scope.OwnedPaths)
	markStringPresent(out.Present, "review_threshold", out.ReviewThreshold)
	markStringPresent(out.Present, "runtime", out.RuntimePreference)
}

func markStringPresent(present map[string]bool, key, value string) {
	if value != "" {
		present[key] = true
	}
}

func markStringSlicePresent(present map[string]bool, key string, values []string) {
	if len(values) > 0 {
		present[key] = true
	}
}

func normalizeStatus(s eposticket.Status) Status {
	switch s {
	case eposticket.StatusPending, eposticket.StatusRepairPending:
		return StatusOpen
	case eposticket.StatusClaimed, eposticket.StatusImplementing,
		eposticket.StatusVerifying, eposticket.StatusUnderReview:
		return StatusInProgress
	case eposticket.StatusDone, eposticket.StatusFailed:
		return StatusClosed
	case eposticket.StatusHeld:
		return StatusBlocked
	}
	return Status(s)
}

func isExtendedStatus(s eposticket.Status) bool {
	switch s {
	case eposticket.StatusPending, eposticket.StatusClaimed, eposticket.StatusImplementing,
		eposticket.StatusVerifying, eposticket.StatusUnderReview, eposticket.StatusRepairPending,
		eposticket.StatusHeld, eposticket.StatusDone, eposticket.StatusFailed:
		return true
	default:
		return false
	}
}

func cloneBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyNamedString(out map[string]any, key, value string, present map[string]bool) {
	if value != "" || present[key] {
		out[key] = value
	}
}

func copyNamedInt(out map[string]any, key string, value int, present map[string]bool) {
	if value != 0 || present[key] {
		out[key] = value
	}
}

func copyNamedStringSlice(out map[string]any, key string, value []string, present map[string]bool) {
	if len(value) > 0 || present[key] {
		out[key] = append([]string(nil), value...)
	}
}

func copyNamedSlice[T any](out map[string]any, key string, value []T, present map[string]bool) {
	if len(value) > 0 || present[key] {
		out[key] = append([]T(nil), value...)
	}
}

func copyNamedStruct[T any](out map[string]any, key string, value T, present map[string]bool) {
	var zero T
	if !reflect.DeepEqual(value, zero) || present[key] {
		out[key] = value
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		if t == nil {
			return ""
		}
		return fmt.Sprint(t)
	}
}

func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

func asStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, asString(item))
		}
		return out
	default:
		return nil
	}
}

func asTyped[T any](v any) T {
	if typed, ok := v.(T); ok {
		return typed
	}
	var out T
	data, err := yaml.Marshal(v)
	if err != nil {
		return out
	}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return out
	}
	return out
}

func asTypedSlice[T any](v any) []T {
	if typed, ok := v.([]T); ok {
		return append([]T(nil), typed...)
	}
	var out []T
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil
	}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}
