package engine

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"verk/internal/adapters/repo/git"
)

// reviewBaseline captures the dirty state already present when a worker starts.
// It records which files were already changed relative to baseCommit so that
// collectReviewDelta can exclude them (unless the worker further modifies them).
type reviewBaseline struct {
	BaseCommit         string
	PreExistingChanged []string
	Files              map[string]reviewFileSnapshot
}

// reviewFileSnapshot holds the content snapshot of a single file at baseline
// capture time.
type reviewFileSnapshot struct {
	Path   string
	Exists bool
	Bytes  []byte
}

// reviewDelta holds the files and diff representing what a worker actually
// changed during one attempt.
type reviewDelta struct {
	ChangedFiles []string
	Diff         string
}

// captureReviewBaseline snapshots the dirty state of repoRoot relative to
// baseCommit before the worker runs. Engine-owned paths (.verk/, .tickets/,
// .git/) are excluded so they never pollute the review.
func captureReviewBaseline(repoRoot, baseCommit string) (reviewBaseline, error) {
	changed, err := collectChangedFiles(repoRoot, baseCommit)
	if err != nil {
		return reviewBaseline{}, fmt.Errorf("captureReviewBaseline: collect changed files: %w", err)
	}

	snapshots := make(map[string]reviewFileSnapshot, len(changed))
	for _, rel := range changed {
		abs := filepath.Join(repoRoot, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			if os.IsNotExist(err) {
				snapshots[rel] = reviewFileSnapshot{Path: rel, Exists: false}
				continue
			}
			return reviewBaseline{}, fmt.Errorf("captureReviewBaseline: read %s: %w", rel, err)
		}
		snapshots[rel] = reviewFileSnapshot{Path: rel, Exists: true, Bytes: data}
	}

	preExisting := append([]string(nil), changed...)
	sort.Strings(preExisting)

	return reviewBaseline{
		BaseCommit:         baseCommit,
		PreExistingChanged: preExisting,
		Files:              snapshots,
	}, nil
}

// collectReviewDelta computes the files and diff representing what changed
// during a worker attempt relative to the captured baseline.
//
// Attribution rule: a pre-existing dirty file is included only when its
// current state (content or existence) differs from the baseline snapshot.
// If it is unchanged the worker did not touch it, so it is excluded.
//
// Limitation: in a shared worktree, if a concurrent worker modifies the same
// pre-existing dirty file during this attempt, the engine cannot attribute
// ownership with perfect certainty. Existing scope validation prevents
// overlapping ownership; this helper handles normal pre-existing dirty state
// and disjoint parallel work.
func collectReviewDelta(repoRoot, baseCommit string, baseline reviewBaseline) (reviewDelta, error) {
	afterChanged, err := collectChangedFiles(repoRoot, baseCommit)
	if err != nil {
		return reviewDelta{}, fmt.Errorf("collectReviewDelta: collect changed files: %w", err)
	}

	preExistingSet := make(map[string]struct{}, len(baseline.PreExistingChanged))
	for _, f := range baseline.PreExistingChanged {
		preExistingSet[f] = struct{}{}
	}

	var workerChanged []string
	for _, f := range afterChanged {
		if _, wasPreExisting := preExistingSet[f]; !wasPreExisting {
			// File was clean or absent at baseline — any current change is
			// attributable to the worker.
			workerChanged = append(workerChanged, f)
			continue
		}

		// File was already dirty at baseline. Include it only if the worker
		// further changed it.
		snap := baseline.Files[f]
		abs := filepath.Join(repoRoot, f)
		current, readErr := os.ReadFile(abs)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				if snap.Exists {
					// File existed at baseline but worker deleted it — include.
					workerChanged = append(workerChanged, f)
				}
				// If !snap.Exists and not present now: unchanged, skip.
				continue
			}
			return reviewDelta{}, fmt.Errorf("collectReviewDelta: read %s: %w", f, readErr)
		}
		if !snap.Exists || !bytes.Equal(snap.Bytes, current) {
			workerChanged = append(workerChanged, f)
		}
		// If snap.Exists && bytes.Equal: worker did not change it — exclude.
	}

	// Also check whether a pre-existing dirty file was reverted to clean by
	// the worker (it won't appear in afterChanged but was in the baseline).
	// A revert is a worker action, so include it.
	for _, f := range baseline.PreExistingChanged {
		// Skip files that are still showing as changed — already handled above.
		alreadyIncluded := false
		for _, w := range workerChanged {
			if w == f {
				alreadyIncluded = true
				break
			}
		}
		if alreadyIncluded {
			continue
		}
		// If the file is no longer in afterChanged, either the worker reverted it
		// or removed it. Either way, that is a worker action.
		found := false
		for _, a := range afterChanged {
			if a == f {
				found = true
				break
			}
		}
		if !found {
			workerChanged = append(workerChanged, f)
		}
	}

	workerChanged = dedupeAndSortChanged(workerChanged)

	if len(workerChanged) == 0 {
		return reviewDelta{ChangedFiles: nil, Diff: ""}, nil
	}

	repo, err := git.New(repoRoot)
	if err != nil {
		return reviewDelta{}, fmt.Errorf("collectReviewDelta: open repo: %w", err)
	}
	diff, err := repo.DiffAgainstFiles(baseCommit, workerChanged)
	if err != nil {
		return reviewDelta{}, fmt.Errorf("collectReviewDelta: diff: %w", err)
	}

	return reviewDelta{
		ChangedFiles: workerChanged,
		Diff:         diff,
	}, nil
}
