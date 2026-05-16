package epos

import "testing"

func TestDetectProfile_SecurityTagWins(t *testing.T) {
	ticket := Ticket{
		ID:     "sec-1",
		Status: StatusOpen,
		UnknownFrontmatter: map[string]any{
			"tags": []string{"security", "backend"},
		},
	}
	if got := DetectProfile(ticket); got != ProfileSecurity {
		t.Fatalf("DetectProfile = %q, want %q", got, ProfileSecurity)
	}
}

func TestDetectProfile_ContractFromExitCodeCriterion(t *testing.T) {
	ticket := Ticket{
		ID:                 "cli-1",
		Status:             StatusOpen,
		AcceptanceCriteria: []string{"Command exits with exit code 0 on success"},
	}
	if got := DetectProfile(ticket); got != ProfileContract {
		t.Fatalf("DetectProfile = %q, want %q", got, ProfileContract)
	}
}

func TestDetectProfile_FrontendFromTSXExtension(t *testing.T) {
	ticket := Ticket{
		ID:         "fe-1",
		Status:     StatusOpen,
		OwnedPaths: []string{"src/components/Button.tsx"},
	}
	if got := DetectProfile(ticket); got != ProfileFrontend {
		t.Fatalf("DetectProfile = %q, want %q", got, ProfileFrontend)
	}
}

func TestDetectProfile_BackendFallback(t *testing.T) {
	ticket := Ticket{
		ID:     "be-1",
		Status: StatusOpen,
		Body:   "# Generic backend task\n\nImplement the storage layer.\n",
	}
	if got := DetectProfile(ticket); got != ProfileBackend {
		t.Fatalf("DetectProfile = %q, want %q", got, ProfileBackend)
	}
}

func TestDetectProfile_SecurityBeatsContract(t *testing.T) {
	// ticket has both an auth tag (security) and a --flag criterion (contract)
	ticket := Ticket{
		ID:     "overlap-1",
		Status: StatusOpen,
		UnknownFrontmatter: map[string]any{
			"tags": []string{"auth"},
		},
		AcceptanceCriteria: []string{"Accepts --flag to override default"},
	}
	if got := DetectProfile(ticket); got != ProfileSecurity {
		t.Fatalf("DetectProfile = %q, want %q (security should beat contract)", got, ProfileSecurity)
	}
}

func TestDetectProfile_NoFalsePositiveFromTitle(t *testing.T) {
	// just a generic feature with no signals → backend
	ticket := Ticket{
		ID:     "generic-1",
		Status: StatusOpen,
		Title:  "Generic feature",
		Body:   "# Generic feature\n\nDo the thing.\n",
	}
	if got := DetectProfile(ticket); got != ProfileBackend {
		t.Fatalf("DetectProfile = %q, want %q", got, ProfileBackend)
	}
}
