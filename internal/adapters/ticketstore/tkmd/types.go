package tkmd

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
	Model              string
	Body               string
	UnknownFrontmatter map[string]any

	present      map[string]bool
	titleDerived bool // true if Title was extracted from body heading, not frontmatter
}
