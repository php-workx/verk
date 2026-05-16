package engine

import (
	"strings"
	"testing"
	"verk/internal/adapters/ticketstore/epos"
	"verk/internal/policy"
	"verk/internal/state"
)

func mkQualityTicket(id, title string) epos.Ticket {
	return epos.Ticket{
		ID:     id,
		Title:  title,
		Status: epos.StatusReady,
	}
}

func mkQualityEpic(id, title string) epos.Ticket {
	return epos.Ticket{
		ID:                 id,
		Title:              title,
		Status:             epos.StatusReady,
		UnknownFrontmatter: map[string]any{"type": "epic"},
	}
}

func findCodes(a state.TicketQualityArtifact) []string {
	out := make([]string, 0, len(a.Findings))
	for _, f := range a.Findings {
		out = append(out, f.Code)
	}
	return out
}

func containsCode(codes []string, want string) bool {
	for _, c := range codes {
		if c == want {
			return true
		}
	}
	return false
}

func TestTicketQuality_MissingAcceptanceCriteriaBlocks(t *testing.T) {
	tk := mkQualityTicket("ver-1", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	in := TicketQualityInput{
		RootTicket: tk,
		Tickets:    []epos.Ticket{tk},
		Config:     policy.DefaultConfig(),
	}
	art := EvaluateTicketQuality(in)
	if !containsCode(findCodes(art), "missing_acceptance_criteria") {
		t.Fatalf("expected missing_acceptance_criteria finding: %+v", art.Findings)
	}
	if !art.Blocked || art.Status != state.TicketQualityBlocked {
		t.Fatalf("expected blocked status: %+v", art)
	}
}

func TestTicketQuality_AmbiguousCriterionWarns(t *testing.T) {
	tk := mkQualityTicket("ver-2", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.AcceptanceCriteria = []string{"feature works properly"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if !containsCode(findCodes(art), "ambiguous_acceptance_criterion") {
		t.Fatalf("expected ambiguous_acceptance_criterion finding: %+v", art.Findings)
	}
}

func TestTicketQuality_AmbiguousAcceptedWhenConcrete(t *testing.T) {
	tk := mkQualityTicket("ver-2b", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.AcceptanceCriteria = []string{"verk widget --enable exits with status 0 and prints \"ok\" on stdout"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if containsCode(findCodes(art), "ambiguous_acceptance_criterion") {
		t.Fatalf("did not expect ambiguous finding for concrete criterion: %+v", art.Findings)
	}
}

func TestTicketQuality_MissingOwnedPathsBlocks(t *testing.T) {
	tk := mkQualityTicket("ver-3", "Implement widget")
	tk.AcceptanceCriteria = []string{"verk widget --enable exits 0"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if !containsCode(findCodes(art), "missing_owned_paths") {
		t.Fatalf("expected missing_owned_paths: %+v", art.Findings)
	}
}

func TestTicketQuality_DocsTicketDoesNotNeedOwnedPaths(t *testing.T) {
	tk := mkQualityTicket("ver-3b", "Update docs")
	tk.UnknownFrontmatter = map[string]any{"type": "docs"}
	tk.AcceptanceCriteria = []string{"section X explains the new flag --foo"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if containsCode(findCodes(art), "missing_owned_paths") {
		t.Fatalf("did not expect missing_owned_paths on docs ticket: %+v", art.Findings)
	}
}

func TestTicketQuality_MissingDependencyBlocks(t *testing.T) {
	tk := mkQualityTicket("ver-4", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.AcceptanceCriteria = []string{"verk widget exits 0"}
	tk.Deps = []string{"ver-nonexistent"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	codes := findCodes(art)
	if !containsCode(codes, "dependency_missing") {
		t.Fatalf("expected dependency_missing: %+v", art.Findings)
	}
}

func TestTicketQuality_DependencyPresentNoFinding(t *testing.T) {
	parent := mkQualityTicket("ver-5a", "Parent")
	parent.OwnedPaths = []string{"internal/a"}
	parent.AcceptanceCriteria = []string{"parent verk a --run exits 0"}
	child := mkQualityTicket("ver-5b", "Child")
	child.OwnedPaths = []string{"internal/b"}
	child.AcceptanceCriteria = []string{"child verk b --run exits 0"}
	child.Deps = []string{"ver-5a"}
	in := TicketQualityInput{RootTicket: parent, Tickets: []epos.Ticket{parent, child}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if containsCode(findCodes(art), "dependency_missing") {
		t.Fatalf("did not expect dependency_missing when dep exists: %+v", art.Findings)
	}
}

func TestTicketQuality_PublicContractNeedsScenarioBlocks(t *testing.T) {
	tk := mkQualityTicket("ver-6", "Add subcommand for cleanup")
	tk.OwnedPaths = []string{"cmd/verk", "internal/cli"}
	tk.AcceptanceCriteria = []string{"the cleanup subcommand works"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if !containsCode(findCodes(art), "missing_public_contract_scenario") {
		t.Fatalf("expected missing_public_contract_scenario: %+v", art.Findings)
	}
}

func TestTicketQuality_PublicContractWithScenarioPasses(t *testing.T) {
	tk := mkQualityTicket("ver-6b", "Add subcommand cleanup")
	tk.OwnedPaths = []string{"cmd/verk"}
	tk.AcceptanceCriteria = []string{"verk cleanup --force exits 0 and prints \"cleaned\" to stdout"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if containsCode(findCodes(art), "missing_public_contract_scenario") {
		t.Fatalf("did not expect missing_public_contract_scenario: %+v", art.Findings)
	}
}

func TestTicketQuality_DocsDescopeRiskRequiresPlanner(t *testing.T) {
	tk := mkQualityTicket("ver-7", "Document cleanup")
	tk.OwnedPaths = []string{"docs/cleanup.md"}
	tk.Body = "The cleanup subcommand does not support recursive mode."
	tk.AcceptanceCriteria = []string{"docs explain the --force flag"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if !containsCode(findCodes(art), "docs_descope_risk") {
		t.Fatalf("expected docs_descope_risk: %+v", art.Findings)
	}
	for _, f := range art.Findings {
		if f.Code == "docs_descope_risk" && !f.RequiresPlanner {
			t.Fatalf("docs_descope_risk should set RequiresPlanner=true: %+v", f)
		}
	}
}

func TestTicketQuality_DocsDescopeAllowedWithPlanRef(t *testing.T) {
	tk := mkQualityTicket("ver-7b", "Document cleanup")
	tk.OwnedPaths = []string{"docs/cleanup.md"}
	tk.Body = "Per docs/plans/cleanup.md the cleanup subcommand does not support recursive mode."
	tk.AcceptanceCriteria = []string{"docs explain the --force flag"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if containsCode(findCodes(art), "docs_descope_risk") {
		t.Fatalf("did not expect docs_descope_risk when plan referenced: %+v", art.Findings)
	}
}

func TestTicketQuality_MultiSurfaceEpicWithoutIntegrationBlocks(t *testing.T) {
	root := mkQualityEpic("ver-8", "Big feature")
	cliChild := mkQualityTicket("ver-8a", "CLI bits")
	cliChild.OwnedPaths = []string{"internal/cli"}
	cliChild.AcceptanceCriteria = []string{"verk feature --x exits 0"}
	docsChild := mkQualityTicket("ver-8b", "Docs bits")
	docsChild.UnknownFrontmatter = map[string]any{"type": "docs"}
	docsChild.OwnedPaths = []string{"docs/feature.md"}
	docsChild.AcceptanceCriteria = []string{"docs explain --x"}
	in := TicketQualityInput{RootTicket: root, Tickets: []epos.Ticket{root, cliChild, docsChild}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if !containsCode(findCodes(art), "integration_gap") {
		t.Fatalf("expected integration_gap: %+v", art.Findings)
	}
}

func TestTicketQuality_MultiSurfaceEpicWithIntegrationPasses(t *testing.T) {
	root := mkQualityEpic("ver-9", "Big feature")
	cliChild := mkQualityTicket("ver-9a", "CLI bits")
	cliChild.OwnedPaths = []string{"internal/cli"}
	cliChild.AcceptanceCriteria = []string{"verk feature --x exits 0"}
	docsChild := mkQualityTicket("ver-9b", "Docs bits")
	docsChild.UnknownFrontmatter = map[string]any{"type": "docs"}
	docsChild.OwnedPaths = []string{"docs/feature.md"}
	docsChild.AcceptanceCriteria = []string{"docs explain --x"}
	e2eChild := mkQualityTicket("ver-9c", "Integration coverage")
	e2eChild.OwnedPaths = []string{"internal/e2e"}
	e2eChild.AcceptanceCriteria = []string{"e2e verk feature --x exits 0"}
	in := TicketQualityInput{RootTicket: root, Tickets: []epos.Ticket{root, cliChild, docsChild, e2eChild}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if containsCode(findCodes(art), "integration_gap") {
		t.Fatalf("did not expect integration_gap with e2e child: %+v", art.Findings)
	}
}

func TestTicketQuality_PassesWhenAllRulesMet(t *testing.T) {
	tk := mkQualityTicket("ver-pass", "Healthy ticket")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.AcceptanceCriteria = []string{"verk widget --enable exits 0 and writes /tmp/widget.log"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if art.Blocked {
		t.Fatalf("did not expect blocked: %+v", art)
	}
	if art.Status != state.TicketQualityPassed {
		t.Fatalf("expected passed status, got %q", art.Status)
	}
	if len(art.Findings) != 0 {
		t.Fatalf("expected no findings: %+v", art.Findings)
	}
}

func TestTicketQuality_FindingIDStable(t *testing.T) {
	tk := mkQualityTicket("ver-id", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	a := EvaluateTicketQuality(in)
	b := EvaluateTicketQuality(in)
	if len(a.Findings) == 0 || len(b.Findings) == 0 {
		t.Fatalf("expected findings")
	}
	if a.Findings[0].ID != b.Findings[0].ID {
		t.Fatalf("finding ids should be stable: %q vs %q", a.Findings[0].ID, b.Findings[0].ID)
	}
	if !strings.HasPrefix(a.Findings[0].ID, "missing_acceptance_criteria-") {
		t.Fatalf("finding id should be prefixed with code: %q", a.Findings[0].ID)
	}
}

func TestTicketQualityRepair_EpicGetsUnionOwnedPaths(t *testing.T) {
	root := mkQualityEpic("ver-epic", "Epic root")
	root.OwnedPaths = nil
	c1 := mkQualityTicket("ver-c1", "Child 1")
	c1.OwnedPaths = []string{"internal/a", "internal/b"}
	c1.AcceptanceCriteria = []string{"verk a --do exits 0"}
	c2 := mkQualityTicket("ver-c2", "Child 2")
	c2.OwnedPaths = []string{"internal/b", "docs/x.md"}
	c2.AcceptanceCriteria = []string{"verk b --do exits 0"}
	c3 := mkQualityTicket("ver-c3", "Integration")
	c3.OwnedPaths = []string{"internal/e2e"}
	c3.AcceptanceCriteria = []string{"e2e verk integration --check exits 0"}
	in := TicketQualityInput{RootTicket: root, Tickets: []epos.Ticket{root, c1, c2, c3}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	plan := BuildTicketQualityRepairPlan(in, art)
	repaired, ok := plan.Tickets[root.ID]
	if !ok {
		t.Fatalf("expected repaired epic ticket: %+v", plan)
	}
	wantPaths := []string{"docs/x.md", "internal/a", "internal/b", "internal/e2e"}
	if len(repaired.OwnedPaths) != len(wantPaths) {
		t.Fatalf("expected owned paths %v, got %v", wantPaths, repaired.OwnedPaths)
	}
	for i, p := range wantPaths {
		if repaired.OwnedPaths[i] != p {
			t.Fatalf("expected path[%d]=%q got %q", i, p, repaired.OwnedPaths[i])
		}
	}
}

func TestTicketQualityRepair_EpicNoRepairWhenChildMissingPaths(t *testing.T) {
	root := mkQualityEpic("ver-epic2", "Epic root")
	c1 := mkQualityTicket("ver-c1", "Child 1")
	c1.OwnedPaths = []string{"internal/a"}
	c1.AcceptanceCriteria = []string{"verk a --do exits 0"}
	c2 := mkQualityTicket("ver-c2", "Child 2")
	c2.AcceptanceCriteria = []string{"verk b --do exits 0"}
	in := TicketQualityInput{RootTicket: root, Tickets: []epos.Ticket{root, c1, c2}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	plan := BuildTicketQualityRepairPlan(in, art)
	if _, ok := plan.Tickets[root.ID]; ok {
		t.Fatalf("expected no epic repair when child lacks owned paths: %+v", plan)
	}
}

func TestTicketQualityRepair_PublicContractScenarioNotAutoRewritten(t *testing.T) {
	tk := mkQualityTicket("ver-pcs", "Add subcommand")
	tk.OwnedPaths = []string{"cmd/verk"}
	tk.AcceptanceCriteria = []string{"the cleanup subcommand works"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	plan := BuildTicketQualityRepairPlan(in, art)
	if _, ok := plan.Tickets[tk.ID]; ok {
		t.Fatalf("did not expect auto-repair for public contract scenario gap: %+v", plan)
	}
}

func TestTicketQualityRepair_AmbiguousCriterionNotRewritten(t *testing.T) {
	tk := mkQualityTicket("ver-ambig", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.AcceptanceCriteria = []string{"feature works properly"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	plan := BuildTicketQualityRepairPlan(in, art)
	if _, ok := plan.Tickets[tk.ID]; ok {
		t.Fatalf("did not expect ambiguous criterion auto-rewrite: %+v", plan)
	}
}

func TestTicketQualityTraceability_ExtractsSourceRefFromBody(t *testing.T) {
	tk := mkQualityTicket("ver-tr1", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.Body = "See docs/plans/widget.md for the full spec."
	tk.AcceptanceCriteria = []string{"verk widget --enable exits 0"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if len(art.Traces) == 0 {
		t.Fatalf("expected at least one trace, got none")
	}
	if art.Traces[0].SourceRef != "docs/plans/widget.md" {
		t.Fatalf("expected SourceRef %q, got %q", "docs/plans/widget.md", art.Traces[0].SourceRef)
	}
}

func TestTicketQualityTraceability_ExtractsSourceRefFromFrontmatter(t *testing.T) {
	tk := mkQualityTicket("ver-tr2", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.UnknownFrontmatter = map[string]any{
		"plan_refs": []any{"docs/plans/widget-plan.md", "docs/plans/other.md"},
	}
	tk.AcceptanceCriteria = []string{"verk widget --enable exits 0"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if len(art.Traces) == 0 {
		t.Fatalf("expected at least one trace, got none")
	}
	if art.Traces[0].SourceRef != "docs/plans/widget-plan.md" {
		t.Fatalf("expected SourceRef %q, got %q", "docs/plans/widget-plan.md", art.Traces[0].SourceRef)
	}
}

func TestTicketQualityTraceability_PopulatesValidationRefs(t *testing.T) {
	tk := mkQualityTicket("ver-tr3", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.AcceptanceCriteria = []string{"verk widget --enable exits 0"}
	tk.TestCases = []string{"test_widget_enable"}
	tk.ValidationCommands = []string{"go test ./internal/widget/..."}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if len(art.Traces) == 0 {
		t.Fatalf("expected at least one trace, got none")
	}
	refs := art.Traces[0].ValidationRefs
	if len(refs) != 2 {
		t.Fatalf("expected 2 validation refs, got %d: %v", len(refs), refs)
	}
	if refs[0] != "test_widget_enable" {
		t.Fatalf("expected test case first, got %q", refs[0])
	}
	if refs[1] != "go test ./internal/widget/..." {
		t.Fatalf("expected validation command second, got %q", refs[1])
	}
}

func TestTicketQualityTraceability_MarksPublicContract(t *testing.T) {
	tk := mkQualityTicket("ver-tr4", "Add subcommand cleanup")
	tk.OwnedPaths = []string{"cmd/verk"}
	tk.AcceptanceCriteria = []string{"verk cleanup --force exits 0 and prints \"cleaned\" to stdout"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if len(art.Traces) == 0 {
		t.Fatalf("expected at least one trace, got none")
	}
	if !art.Traces[0].PublicContract {
		t.Fatalf("expected PublicContract=true for cmd/ owned ticket, got false")
	}
}

func TestTicketQualityTraceability_OneTracePerCriterion(t *testing.T) {
	tk := mkQualityTicket("ver-tr5", "Implement widget")
	tk.OwnedPaths = []string{"internal/widget"}
	tk.AcceptanceCriteria = []string{
		"verk widget --enable exits 0",
		"verk widget --disable exits 0",
		"verk widget --status prints enabled or disabled on stdout",
	}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	if len(art.Traces) != 3 {
		t.Fatalf("expected 3 traces (one per criterion), got %d", len(art.Traces))
	}
	for i, tr := range art.Traces {
		if tr.TicketID != tk.ID {
			t.Fatalf("trace[%d] has wrong TicketID %q", i, tr.TicketID)
		}
		if tr.Criterion != tk.AcceptanceCriteria[i] {
			t.Fatalf("trace[%d] criterion mismatch: want %q got %q", i, tk.AcceptanceCriteria[i], tr.Criterion)
		}
	}
}

func TestTicketQualityRepair_PlannerNoteRecorded(t *testing.T) {
	tk := mkQualityTicket("ver-docs", "Document cleanup")
	tk.OwnedPaths = []string{"docs/cleanup.md"}
	tk.Body = "The cleanup subcommand does not support recursive mode."
	tk.AcceptanceCriteria = []string{"docs explain the --force flag"}
	in := TicketQualityInput{RootTicket: tk, Tickets: []epos.Ticket{tk}, Config: policy.DefaultConfig()}
	art := EvaluateTicketQuality(in)
	plan := BuildTicketQualityRepairPlan(in, art)
	repaired, ok := plan.Tickets[tk.ID]
	if !ok {
		t.Fatalf("expected planner-note repair: %+v", plan)
	}
	notes, _ := repaired.UnknownFrontmatter["ticket_quality_notes"].([]any)
	if len(notes) == 0 {
		t.Fatalf("expected at least one ticket_quality_notes entry: %+v", repaired.UnknownFrontmatter)
	}
}
