package epos

import "fmt"

type Status string

const (
	StatusOpen       Status = "open"
	StatusReady      Status = "ready"
	StatusInProgress Status = "in_progress"
	StatusBlocked    Status = "blocked"
	StatusClosed     Status = "closed"
)

type Ticket struct {
	ID                 string
	Title              string
	Status             Status
	Deps               []string
	Priority           int
	AcceptanceCriteria []string
	TestCases          []string
	ValidationCommands []string
	OwnedPaths         []string
	ReviewThreshold    string
	Runtime            string
	// Model is retained for backward compatibility only and is ignored by
	// execution routing. Execution model and reasoning are policy/config-owned
	// and controlled via runtime role profiles.
	Model string
	// Profile identifies the agent role profile to use for this ticket.
	// Empty means use the default/detected profile.
	Profile            string
	Body               string
	UnknownFrontmatter map[string]any

	present      map[string]bool
	titleDerived bool
}

const (
	ProfileSecurity = "security-engineer"
	ProfileContract = "contract-engineer"
	ProfileFrontend = "frontend-engineer"
	ProfileBackend  = "backend-engineer"
)

// ValidateProfile returns nil for empty or known profile values, error otherwise.
func ValidateProfile(p string) error {
	if p == "" {
		return nil
	}
	switch p {
	case ProfileSecurity, ProfileContract, ProfileFrontend, ProfileBackend:
		return nil
	}
	return fmt.Errorf("unknown profile %q", p)
}
