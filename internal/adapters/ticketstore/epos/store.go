package epos

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"verk/internal/state"

	eposticket "github.com/php-workx/epos/ticket"
	eposmarkdown "github.com/php-workx/epos/ticket/markdown"
	eposruntime "github.com/php-workx/epos/ticket/runtime"
	eposstore "github.com/php-workx/epos/ticket/store"
	"gopkg.in/yaml.v3"
)

var canonicalStatuses = map[Status]struct{}{
	StatusOpen:       {},
	StatusReady:      {},
	StatusInProgress: {},
	StatusBlocked:    {},
	StatusClosed:     {},
}

func LoadTicket(path string) (Ticket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Ticket{}, fmt.Errorf("read ticket: %w", err)
	}
	_, body, err := splitFrontMatter(data)
	if err != nil {
		return Ticket{}, err
	}
	parsed, err := eposmarkdown.UnmarshalTicket(data)
	if err != nil {
		return Ticket{}, err
	}
	ticket := fromEpos(parsed)
	ticket.Body = string(body)
	return ticket, nil
}

func SaveTicket(path string, ticket Ticket) error {
	if err := validateTicketWritable(ticket); err != nil {
		return err
	}
	eposTicket := toEpos(ticket)
	node, err := eposmarkdown.MarshalYAML(eposTicket)
	if err != nil {
		return fmt.Errorf("marshal ticket frontmatter: %w", err)
	}
	yamlBytes, err := yaml.Marshal(node)
	if err != nil {
		return fmt.Errorf("marshal ticket yaml: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.Write(yamlBytes)
	if len(yamlBytes) == 0 || yamlBytes[len(yamlBytes)-1] != '\n' {
		buf.WriteByte('\n')
	}
	buf.WriteString("---\n")
	buf.WriteString(ticket.Body)
	if err := state.SaveFileAtomic(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write ticket: %w", err)
	}
	return nil
}

func ValidateTicketSchedulingFields(ticket Ticket) error {
	if ticket.Status == "" {
		return fmt.Errorf("invalid ticket status %q", ticket.Status)
	}
	return validateTicketWritable(ticket)
}

func HasChildren(rootDir, ticketID string) (bool, error) {
	children, err := ListAllChildren(rootDir, ticketID)
	if err != nil {
		return false, err
	}
	return len(children) > 0, nil
}

func ListAllChildren(rootDir, parentID string) ([]Ticket, error) {
	ticketsDir := resolveTicketsDir(rootDir)
	parentPath := filepath.Join(ticketsDir, parentID+".md")
	if _, err := os.Stat(parentPath); err != nil {
		return nil, fmt.Errorf("load %s: %w", parentID, err)
	}

	epicDeps, err := loadEpicChildren(ticketsDir, parentID)
	if err != nil {
		return nil, fmt.Errorf("load epic children for %s: %w", parentID, err)
	}

	paths, err := filepath.Glob(filepath.Join(ticketsDir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob tickets: %w", err)
	}
	sort.Strings(paths)

	children := []Ticket{}
	seen := map[string]struct{}{}
	for _, path := range paths {
		ticket, err := LoadTicket(path)
		if err != nil {
			return nil, err
		}
		if !isChildOf(ticket, parentID, epicDeps) {
			continue
		}
		if _, ok := seen[ticket.ID]; ok {
			continue
		}
		seen[ticket.ID] = struct{}{}
		children = append(children, ticket)
	}
	return children, nil
}

func ListReadyChildren(rootDir, parentID string, currentRunID ...string) ([]Ticket, error) {
	ticketsDir := resolveTicketsDir(rootDir)
	children, err := ListAllChildren(rootDir, parentID)
	if err != nil {
		return nil, err
	}
	runID := ""
	if len(currentRunID) > 0 {
		runID = currentRunID[0]
	}
	claimed, err := claimedTickets(rootDir, runID)
	if err != nil {
		return nil, err
	}

	ready := []Ticket{}
	for _, child := range children {
		if !isReadyStatus(child.Status) {
			continue
		}
		ok, err := depsClosed(ticketsDir, child.Deps)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if claimed[child.ID] {
			continue
		}
		ready = append(ready, child)
	}
	return ready, nil
}

func isReadyStatus(status Status) bool {
	status = normalizeStatus(eposticket.Status(status))
	return status == StatusOpen || status == StatusReady
}

func depsClosed(ticketsDir string, deps []string) (bool, error) {
	for _, depID := range deps {
		if depID == "" {
			return false, nil
		}
		dep, err := LoadTicket(filepath.Join(ticketsDir, depID+".md"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if normalizeStatus(eposticket.Status(dep.Status)) != StatusClosed {
			return false, nil
		}
	}
	return true, nil
}

func claimedTickets(rootDir, currentRunID string) (map[string]bool, error) {
	repoRoot := resolveRepoRoot(rootDir)
	store, err := eposstore.NewFileStore(repoRoot)
	if err != nil {
		return nil, err
	}
	liveClaims, err := store.ActiveClaimSet()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for ticketID := range liveClaims {
		allowed, err := eposruntime.ClaimAllowsReady(repoRoot, ticketID, currentRunID)
		if err != nil {
			return nil, err
		}
		if allowed {
			continue
		}
		out[ticketID] = true
	}

	durableClaims, err := durableClaimedTickets(repoRoot, currentRunID)
	if err != nil {
		return nil, err
	}
	for ticketID := range durableClaims {
		out[ticketID] = true
	}
	return out, nil
}

func durableClaimedTickets(repoRoot, currentRunID string) (map[string]bool, error) {
	paths, err := filepath.Glob(filepath.Join(repoRoot, ".verk", "runs", "*", "claims", "*.json"))
	if err != nil {
		return nil, fmt.Errorf("glob durable claims: %w", err)
	}
	now := time.Now().UTC()
	out := map[string]bool{}
	for _, path := range paths {
		claim, err := loadClaimArtifact(path)
		if err != nil {
			return nil, err
		}
		if !durableClaimActiveAt(claim, now) {
			continue
		}
		if currentRunID != "" && claim.OwnerRunID == currentRunID {
			continue
		}
		out[claim.TicketID] = true
	}
	return out, nil
}

func durableClaimActiveAt(claim *state.ClaimArtifact, now time.Time) bool {
	if claim == nil || claim.TicketID == "" || claim.OwnerRunID == "" || claimReleased(claim) {
		return false
	}
	if claim.ExpiresAt.IsZero() {
		return true
	}
	return now.Before(claim.ExpiresAt.UTC())
}

func validateTicketWritable(ticket Ticket) error {
	if ticket.Status != "" {
		if _, ok := canonicalStatuses[ticket.Status]; !ok {
			return fmt.Errorf("invalid ticket status %q", ticket.Status)
		}
	}
	for _, path := range ticket.OwnedPaths {
		if err := validateOwnedPath(path); err != nil {
			return err
		}
	}
	return nil
}

func validateOwnedPath(path string) error {
	if path == "" {
		return errors.New("owned_paths contains an empty path")
	}
	if strings.ContainsAny(path, "*?[]") {
		return fmt.Errorf("owned_paths rejects glob pattern %q", path)
	}
	if strings.Contains(path, "\\") {
		return fmt.Errorf("owned_paths rejects path separator %q", path)
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("owned_paths rejects absolute path %q", path)
	}
	cleaned := filepath.Clean(path)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return fmt.Errorf("owned_paths escapes repo root: %q", path)
	}
	return nil
}

func parentOf(ticket Ticket) string {
	raw, ok := ticket.UnknownFrontmatter["parent"]
	if !ok {
		return ""
	}
	parent, _ := raw.(string)
	return parent
}

func hasEpicType(ticket Ticket) bool {
	raw, ok := ticket.UnknownFrontmatter["type"]
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(asString(raw)), "epic")
}

func loadEpicChildren(ticketsDir, parentID string) (map[string]struct{}, error) {
	children := map[string]struct{}{}
	parent, err := LoadTicket(filepath.Join(ticketsDir, parentID+".md"))
	if err != nil {
		return children, err
	}
	if !hasEpicType(parent) {
		return children, nil
	}
	for _, dep := range parent.Deps {
		children[dep] = struct{}{}
	}
	return children, nil
}

func isChildOf(ticket Ticket, parentID string, epicDeps map[string]struct{}) bool {
	if ticket.ID == parentID {
		return false
	}
	if parentOf(ticket) == parentID {
		return true
	}
	_, ok := epicDeps[ticket.ID]
	return ok
}

// ErrEpicCycle is returned when a cycle is detected in the epic child graph
// (e.g. ticket A is both an ancestor and a descendant of itself).
var ErrEpicCycle = errors.New("epic cycle detected")

// DetectEpicCycle returns ErrEpicCycle if epicID already appears in ancestors,
// indicating a circular epic-child relationship.
func DetectEpicCycle(epicID string, ancestors map[string]struct{}) error {
	if _, cycle := ancestors[epicID]; cycle {
		return fmt.Errorf("%w: %q appears in its own descendant chain", ErrEpicCycle, epicID)
	}
	return nil
}

func resolveTicketsDir(rootDir string) string {
	cleaned := filepath.Clean(rootDir)
	if filepath.Base(cleaned) == ".tickets" {
		return cleaned
	}
	return filepath.Join(cleaned, ".tickets")
}

func splitFrontMatter(data []byte) ([]byte, []byte, error) {
	line, next, ok := nextLine(data, 0)
	if !ok {
		return nil, nil, errors.New("missing frontmatter")
	}
	if strings.TrimSpace(string(line)) != "---" {
		return nil, nil, errors.New("missing frontmatter")
	}

	frontmatterStart := next
	pos := next
	for {
		if pos >= len(data) {
			return nil, nil, errors.New("missing closing frontmatter delimiter")
		}
		line, next, ok = nextLine(data, pos)
		if !ok {
			return nil, nil, errors.New("missing closing frontmatter delimiter")
		}
		if strings.TrimSpace(string(line)) == "---" {
			return data[frontmatterStart:pos], data[next:], nil
		}
		pos = next
	}
}

func nextLine(data []byte, start int) ([]byte, int, bool) {
	if start >= len(data) {
		return nil, start, false
	}
	for i := start; i < len(data); i++ {
		if data[i] == '\n' {
			return data[start:i], i + 1, true
		}
	}
	return data[start:], len(data), true
}
