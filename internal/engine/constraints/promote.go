package constraints

import (
	"fmt"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// CandidateInfo summarizes a candidate for promotion.
type CandidateInfo struct {
	Signature       Signature
	DistinctTickets int
	LastFinding     string // title of most recent finding (not tracked in v1 candidates)
}

// ListCandidates returns all candidates sorted by distinct-ticket count desc.
func ListCandidates(store *Store) ([]CandidateInfo, error) {
	// Scan candidate files to build list.
	// candidatesDir may not exist yet.
	entries, err := candidateDirEntries(store.candidatesDir)
	if err != nil {
		return nil, err
	}

	infos := make([]CandidateInfo, 0, len(entries))
	for _, sig := range entries {
		count, err := store.DistinctTicketCount(sig)
		if err != nil {
			return nil, fmt.Errorf("count candidate %s: %w", sig, err)
		}
		infos = append(infos, CandidateInfo{
			Signature:       sig,
			DistinctTickets: count,
		})
	}

	sort.Slice(infos, func(i, j int) bool {
		if infos[i].DistinctTickets != infos[j].DistinctTickets {
			return infos[i].DistinctTickets > infos[j].DistinctTickets
		}
		return infos[i].Signature < infos[j].Signature
	})

	return infos, nil
}

// PromoteCandidate writes a new Constraint entry into the index.
// specYAML is operator-authored YAML matching CheckSpec.
func PromoteCandidate(store *Store, sig Signature, specYAML string) error {
	var rawSpec map[string]interface{}
	if err := yaml.Unmarshal([]byte(specYAML), &rawSpec); err != nil {
		return fmt.Errorf("parse spec YAML: %w", err)
	}

	checkType, ok := rawSpec["type"].(string)
	if !ok || checkType == "" {
		return fmt.Errorf("spec YAML must have a 'type' field (command or grep)")
	}

	specBody, hasSpec := rawSpec["spec"]
	if !hasSpec {
		return fmt.Errorf("spec YAML must have a 'spec' field")
	}

	timeoutMs := 30000
	if t, ok := rawSpec["timeout_ms"]; ok {
		switch v := t.(type) {
		case int:
			timeoutMs = v
		case float64:
			timeoutMs = int(v)
		}
	}

	idx, err := store.Load()
	if err != nil {
		return fmt.Errorf("load constraint index: %w", err)
	}

	count, err := store.DistinctTicketCount(sig)
	if err != nil {
		return fmt.Errorf("count candidate tickets: %w", err)
	}

	c := Constraint{
		ID:                string(sig),
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
		PromotedFromCount: count,
		Check: CheckSpec{
			Type:      checkType,
			Spec:      specBody,
			TimeoutMs: timeoutMs,
		},
		Active:              false, // operator must enable
		ActivationThreshold: 3,
	}

	// Check for duplicate.
	for _, existing := range idx.Constraints {
		if existing.ID == c.ID {
			return fmt.Errorf("constraint %q already exists in index", c.ID)
		}
	}

	idx.Constraints = append(idx.Constraints, c)
	return store.Save(idx)
}

// candidateDirEntries lists Signature values for all .jsonl files in candidatesDir.
func candidateDirEntries(dir string) ([]Signature, error) {
	entries, err := listDir(dir)
	if err != nil {
		return nil, err
	}
	sigs := make([]Signature, 0, len(entries))
	for _, e := range entries {
		if len(e) > 6 && e[len(e)-6:] == ".jsonl" {
			sigs = append(sigs, Signature(e[:len(e)-6]))
		}
	}
	return sigs, nil
}
