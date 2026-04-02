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
	Model              string
	Body               string
	UnknownFrontmatter map[string]any

	present map[string]bool
}
