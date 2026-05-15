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
