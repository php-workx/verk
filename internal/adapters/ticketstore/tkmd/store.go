package tkmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"verk/internal/state"
)

var canonicalStatuses = map[Status]struct{}{
	StatusOpen:       {},
	StatusReady:      {}, // kept for backward compat — treated same as open
	StatusInProgress: {},
	StatusBlocked:    {},
	StatusClosed:     {},
}

type claimRecord struct {
	TicketID   string    `json:"ticket_id"`
	OwnerRunID string    `json:"owner_run_id"`
	LeaseID    string    `json:"lease_id"`
	ExpiresAt  time.Time `json:"expires_at"`
	State      string    `json:"state"`
}

func LoadTicket(path string) (Ticket, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Ticket{}, fmt.Errorf("read ticket: %w", err)
	}

	frontmatter, body, err := splitFrontMatter(data)
	if err != nil {
		return Ticket{}, err
	}

	ticket := &Ticket{
		Body:               string(body),
		UnknownFrontmatter: map[string]any{},
		present:            map[string]bool{},
	}
	if err := decodeFrontMatter(frontmatter, ticket); err != nil {
		return Ticket{}, err
	}
	// Extract title from body's first # heading if not in frontmatter
	if ticket.Title == "" {
		ticket.Title = extractHeadingTitle(ticket.Body)
		ticket.titleDerived = true
	}
	return *ticket, nil
}

func SaveTicket(path string, ticket Ticket) error {
	if err := validateTicketWritable(ticket); err != nil {
		return err
	}

	frontmatter := encodeFrontMatter(&ticket)
	var buf bytes.Buffer
	buf.WriteString("---\n")
	buf.WriteString(frontmatter)
	buf.WriteString("---\n")
	buf.WriteString(ticket.Body)

	if err := state.SaveFileAtomic(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write ticket: %w", err)
	}
	return nil
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

func ListReadyChildren(rootDir, parentID string, currentRunID ...string) ([]Ticket, error) {
	ticketsDir := resolveTicketsDir(rootDir)
	paths, err := filepath.Glob(filepath.Join(ticketsDir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob tickets: %w", err)
	}
	sort.Strings(paths)

	runID := ""
	if len(currentRunID) > 0 {
		runID = currentRunID[0]
	}

	// Load the epic's deps list for alternative child discovery.
	epicDeps, err := loadEpicChildren(ticketsDir, parentID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load epic children for %s: %w", parentID, err)
	}

	var ready []Ticket
	for _, path := range paths {
		ticket, err := LoadTicket(path)
		if err != nil {
			return nil, err
		}
		if ticket.ID == parentID {
			continue
		}
		isChild := parentOf(&ticket) == parentID
		if !isChild {
			_, isChild = epicDeps[ticket.ID]
		}
		if !isChild {
			continue
		}
		if ticket.Status != StatusOpen && ticket.Status != StatusReady {
			continue
		}
		ok, err := depsClosed(ticketsDir, ticket.Deps)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ok, err = claimAllowsReady(ticketsDir, ticket.ID, runID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		ready = append(ready, ticket)
	}
	return ready, nil
}

func ValidateTicketSchedulingFields(ticket Ticket) error {
	if _, ok := canonicalStatuses[ticket.Status]; !ok {
		return fmt.Errorf("invalid ticket status %q", ticket.Status)
	}
	for _, path := range ticket.OwnedPaths {
		if err := validateOwnedPath(path); err != nil {
			return err
		}
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

func parentOf(ticket *Ticket) string {
	if ticket == nil || ticket.UnknownFrontmatter == nil {
		return ""
	}
	raw, ok := ticket.UnknownFrontmatter["parent"]
	if !ok {
		return ""
	}
	parent, _ := raw.(string)
	return parent
}

// HasChildren reports whether the ticket with the given ID has any children.
// A child is a ticket whose parent field, deps, or links reference the given ID.
func HasChildren(rootDir, ticketID string) (bool, error) {
	ticketsDir := resolveTicketsDir(rootDir)
	paths, err := filepath.Glob(filepath.Join(ticketsDir, "*.md"))
	if err != nil {
		return false, fmt.Errorf("glob tickets: %w", err)
	}
	epicDeps, err := loadEpicChildren(ticketsDir, ticketID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("load children of %s: %w", ticketID, err)
	}
	for _, path := range paths {
		ticket, err := LoadTicket(path)
		if err != nil {
			return false, err
		}
		if ticket.ID == ticketID {
			continue
		}
		if parentOf(&ticket) == ticketID {
			return true, nil
		}
		if _, ok := epicDeps[ticket.ID]; ok {
			return true, nil
		}
	}
	return false, nil
}

// ListAllChildren returns all direct children of the ticket with the given ID,
// regardless of their status. Used by the engine to discover the full hierarchy.
// Returns an error if the parent ticket does not exist.
func ListAllChildren(rootDir, parentID string) ([]Ticket, error) {
	ticketsDir := resolveTicketsDir(rootDir)
	parentPath := filepath.Join(ticketsDir, parentID+".md")
	if _, err := os.Stat(parentPath); err != nil {
		return nil, fmt.Errorf("load %s: %w", parentID, err)
	}
	epicDeps, err := loadEpicChildren(ticketsDir, parentID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load epic children for %s: %w", parentID, err)
	}
	paths, err := filepath.Glob(filepath.Join(ticketsDir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob tickets: %w", err)
	}
	sort.Strings(paths)
	var children []Ticket
	seen := make(map[string]struct{})
	for _, path := range paths {
		ticket, err := LoadTicket(path)
		if err != nil {
			return nil, err
		}
		if ticket.ID == parentID {
			continue
		}
		isChild := parentOf(&ticket) == parentID
		if !isChild {
			_, isChild = epicDeps[ticket.ID]
		}
		if !isChild {
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

// extractHeadingTitle extracts the title from the first # heading in the body.
func extractHeadingTitle(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

// loadEpicChildren loads an epic ticket's deps and links lists as a set
// for child discovery. Supports three tk conventions: deps (tk dep),
// links (tk link), and parent field on children.
func loadEpicChildren(ticketsDir, epicID string) (map[string]struct{}, error) {
	path := filepath.Join(ticketsDir, epicID+".md")
	ticket, err := LoadTicket(path)
	if err != nil {
		return nil, err
	}
	children := make(map[string]struct{})
	for _, dep := range ticket.Deps {
		children[dep] = struct{}{}
	}
	// Also check links — some projects use tk link instead of tk dep
	if ticket.UnknownFrontmatter != nil {
		links := asStringSlice(ticket.UnknownFrontmatter["links"])
		for _, link := range links {
			children[link] = struct{}{}
		}
	}
	return children, nil
}

func depsClosed(ticketsDir string, deps []string) (bool, error) {
	for _, depID := range deps {
		if depID == "" {
			return false, nil
		}
		depPath := filepath.Join(ticketsDir, depID+".md")
		dep, err := LoadTicket(depPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			return false, err
		}
		if dep.Status != StatusClosed {
			return false, nil
		}
	}
	return true, nil
}

func claimAllowsReady(ticketsDir, ticketID, currentRunID string) (bool, error) {
	claimPath := filepath.Join(ticketsDir, ".claims", ticketID+".json")
	data, err := os.ReadFile(claimPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, fmt.Errorf("read claim: %w", err)
	}

	var claim claimRecord
	if err := json.Unmarshal(data, &claim); err != nil {
		return false, fmt.Errorf("decode claim: %w", err)
	}
	if strings.EqualFold(claim.State, "released") {
		return true, nil
	}
	if claim.OwnerRunID == "" {
		return false, nil
	}
	if !claim.ExpiresAt.IsZero() && !time.Now().UTC().Before(claim.ExpiresAt.UTC()) {
		return true, nil
	}
	if currentRunID != "" && claim.OwnerRunID == currentRunID {
		return true, nil
	}
	return false, nil
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

func decodeFrontMatter(frontmatter []byte, ticket *Ticket) error {
	text := strings.ReplaceAll(string(frontmatter), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		key, value, hasValue, err := splitKeyValue(line)
		if err != nil {
			return err
		}

		if !hasValue {
			nextIndex := nextNonEmptyIndex(lines, i+1)
			if nextIndex < len(lines) && isListItemLine(lines[nextIndex]) {
				var items []any
				for i+1 < len(lines) && isListItemLine(lines[i+1]) {
					items = append(items, parseScalar(listItemValue(lines[i+1])))
					i++
				}
				assignField(ticket, key, items, true)
				continue
			}
			assignField(ticket, key, "", true)
			continue
		}

		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			assignField(ticket, key, parseInlineList(value), true)
			continue
		}
		assignField(ticket, key, parseScalar(value), true)
	}
	return nil
}

func encodeFrontMatter(ticket *Ticket) string {
	var b strings.Builder

	writeScalar := func(key string, value any) {
		b.WriteString(key)
		b.WriteString(": ")
		b.WriteString(formatYAMLValue(value))
		b.WriteByte('\n')
	}

	writeString := func(key, value string) {
		if !fieldPresent(ticket, key) && value == "" {
			return
		}
		writeScalar(key, value)
	}

	writeSlice := func(key string, value []string) {
		if !fieldPresent(ticket, key) && len(value) == 0 {
			return
		}
		b.WriteString(key)
		b.WriteString(": ")
		if len(value) == 0 {
			b.WriteString("[]")
		} else {
			b.WriteString(formatYAMLValue(value))
		}
		b.WriteByte('\n')
	}

	writeString("id", ticket.ID)
	// Only write title to frontmatter if it was originally present there.
	// If the title was derived from a body heading, skip it to preserve round-trip idempotency.
	if !ticket.titleDerived {
		writeString("title", ticket.Title)
	}
	writeString("status", string(ticket.Status))
	writeSlice("deps", ticket.Deps)
	if fieldPresent(ticket, "priority") || ticket.Priority != 0 {
		writeScalar("priority", ticket.Priority)
	}
	writeSlice("acceptance_criteria", ticket.AcceptanceCriteria)
	writeSlice("test_cases", ticket.TestCases)
	writeSlice("validation_commands", ticket.ValidationCommands)
	writeSlice("owned_paths", ticket.OwnedPaths)
	writeString("review_threshold", ticket.ReviewThreshold)
	writeString("runtime", ticket.Runtime)
	writeString("model", ticket.Model)

	known := map[string]struct{}{
		"id": {}, "title": {}, "status": {}, "deps": {}, "priority": {},
		"acceptance_criteria": {}, "test_cases": {}, "validation_commands": {},
		"owned_paths": {}, "review_threshold": {}, "runtime": {}, "model": {},
	}
	var unknownKeys []string
	for key := range ticket.UnknownFrontmatter {
		if _, ok := known[key]; ok {
			continue
		}
		unknownKeys = append(unknownKeys, key)
	}
	sort.Strings(unknownKeys)
	for _, key := range unknownKeys {
		writeScalar(key, ticket.UnknownFrontmatter[key])
	}
	return b.String()
}

func assignField(ticket *Ticket, key string, value any, present bool) { //nolint:unparam // present required by call-site convention, always true currently
	if ticket.present == nil {
		ticket.present = map[string]bool{}
	}
	if present {
		ticket.present[key] = true
	}

	switch key {
	case "id":
		ticket.ID = asString(value)
	case "title":
		ticket.Title = asString(value)
	case "status":
		ticket.Status = Status(asString(value))
	case "deps":
		ticket.Deps = asStringSlice(value)
	case "priority":
		ticket.Priority = asInt(value)
	case "acceptance_criteria":
		ticket.AcceptanceCriteria = asStringSlice(value)
	case "test_cases":
		ticket.TestCases = asStringSlice(value)
	case "validation_commands":
		ticket.ValidationCommands = asStringSlice(value)
	case "owned_paths":
		ticket.OwnedPaths = asStringSlice(value)
	case "review_threshold":
		ticket.ReviewThreshold = asString(value)
	case "runtime":
		ticket.Runtime = asString(value)
	case "model":
		ticket.Model = asString(value)
	default:
		if ticket.UnknownFrontmatter == nil {
			ticket.UnknownFrontmatter = map[string]any{}
		}
		ticket.UnknownFrontmatter[key] = value
	}
}

func fieldPresent(ticket *Ticket, key string) bool {
	return ticket != nil && ticket.present != nil && ticket.present[key]
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func asInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		n, _ := strconv.Atoi(v)
		return n
	default:
		return 0
	}
}

func asStringSlice(value any) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, asString(item))
		}
		return out
	case string:
		if v == "" {
			return []string{}
		}
		return []string{v}
	default:
		return []string{fmt.Sprint(v)}
	}
}

func formatYAMLValue(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case string:
		return formatString(v)
	case Status:
		return formatString(string(v))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case []string:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, item)
		}
		return formatList(items)
	case []any:
		return formatList(v)
	default:
		return formatString(fmt.Sprint(v))
	}
}

func formatList(items []any) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, formatYAMLValue(item))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func formatString(v string) string {
	if v == "" {
		return `""`
	}
	if isPlainString(v) {
		return v
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func isPlainString(v string) bool {
	if v == "" {
		return false
	}
	if strings.TrimSpace(v) != v {
		return false
	}
	if strings.ContainsAny(v, ":#[]{}\\\",'") {
		return false
	}
	for _, r := range v {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '/' && r != '-' && r != '_' &&
			r != '.' && r != ' ' && r != '@' && r != '+' {
			return false
		}
	}
	return true
}

func parseScalar(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return strings.Trim(value, "\"")
	}
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return strings.Trim(value, "'")
	}
	switch value {
	case "null", "~":
		return nil
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.Atoi(value); err == nil {
		return n
	}
	return value
}

func parseInlineList(value string) []any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "[]" {
		return []any{}
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
	if inner == "" {
		return []any{}
	}

	var items []any
	var current strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range inner {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\' && inQuotes:
			escaped = true
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == ',' && !inQuotes:
			items = append(items, parseScalar(current.String()))
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		items = append(items, parseScalar(current.String()))
	}
	return items
}

func splitKeyValue(line string) (string, string, bool, error) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false, fmt.Errorf("invalid frontmatter line %q", line)
	}
	key := strings.TrimSpace(line[:idx])
	value := strings.TrimSpace(line[idx+1:])
	return key, value, value != "", nil
}

func nextNonEmptyIndex(lines []string, start int) int {
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			return i
		}
	}
	return len(lines)
}

func isListItemLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(trimmed, "- ")
}

func listItemValue(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	trimmed = strings.TrimPrefix(trimmed, "-")
	return strings.TrimSpace(trimmed)
}

func nextLine(data []byte, start int) ([]byte, int, bool) {
	if start >= len(data) {
		return nil, start, false
	}
	end := start
	for end < len(data) && data[end] != '\n' && data[end] != '\r' {
		end++
	}
	line := data[start:end]
	if end >= len(data) {
		return line, len(data), true
	}
	if data[end] == '\r' && end+1 < len(data) && data[end+1] == '\n' {
		return line, end + 2, true
	}
	return line, end + 1, true
}
