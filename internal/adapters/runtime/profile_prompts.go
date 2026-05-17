package runtime

import _ "embed"

//go:embed profiles/security-engineer.md
var securityEngineerProfile string

//go:embed profiles/contract-engineer.md
var contractEngineerProfile string

//go:embed profiles/frontend-engineer.md
var frontendEngineerProfile string

//go:embed profiles/backend-engineer.md
var backendEngineerProfile string

// BuildProfilePrompt returns the rationalization framing block for the given
// profile. Unknown profiles return "" — the empty string is a graceful
// degradation; the worker still runs without profile framing.
func BuildProfilePrompt(profile string) string {
	switch profile {
	case "security-engineer":
		return securityEngineerProfile
	case "contract-engineer":
		return contractEngineerProfile
	case "frontend-engineer":
		return frontendEngineerProfile
	case "backend-engineer":
		return backendEngineerProfile
	default:
		return ""
	}
}
