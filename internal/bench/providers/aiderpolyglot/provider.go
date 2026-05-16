// Package aiderpolyglot is an EXPERIMENTAL benchmark provider spike for the
// Aider Polyglot dataset. It is intentionally NOT auto-registered with the
// default provider registry and NOT gated for CI in this iteration.
//
// Operator opt-in is explicit and required:
//
//	p := aiderpolyglot.New()
//	p.DatasetPath = "/path/to/aider-polyglot-dataset"
//	registry.Register(p)
//
// The provider will return ErrNotConfigured from LoadTasks until DatasetPath
// is set. Dataset parsing is not yet implemented (spike placeholder).
package aiderpolyglot

import (
	"errors"
	"verk/internal/bench"
)

// Provider is an experimental Aider Polyglot provider. Not enabled by
// default; not gated for CI in this iteration. Returns ErrNotConfigured
// until the dataset path is wired.
type Provider struct {
	DatasetPath string // populated externally; experimental
}

// New returns a new Provider with an empty DatasetPath.
func New() *Provider { return &Provider{} }

// Name implements bench.Provider.
func (p *Provider) Name() string { return "aider-polyglot" }

// Suites implements bench.Provider.
func (p *Provider) Suites() []bench.SuiteMeta {
	return []bench.SuiteMeta{
		{
			Name:         "smoke",
			Provider:     "aider-polyglot",
			TaskCount:    10,
			SamplingMode: bench.SamplingModeSmoke,
			Description:  "Aider Polyglot smoke subset (experimental)",
		},
	}
}

// ErrNotConfigured is returned by LoadTasks when DatasetPath has not been set.
var ErrNotConfigured = errors.New("aider-polyglot dataset path not configured; set DatasetPath")

// LoadTasks implements bench.Provider.
// Returns ErrNotConfigured if DatasetPath is empty; otherwise returns an error
// indicating that dataset loading is not yet implemented (spike).
func (p *Provider) LoadTasks(suite string) ([]bench.Task, error) {
	if p.DatasetPath == "" {
		return nil, ErrNotConfigured
	}
	// TODO: read dataset metadata from p.DatasetPath; not implemented in this spike.
	return nil, errors.New("aider-polyglot: loading from dataset not yet implemented (spike)")
}

// Capabilities implements bench.Provider.
func (p *Provider) Capabilities() bench.ProviderCapabilities {
	return bench.ProviderCapabilities{
		SupportsIsolatedVerifier: true,
		CachePolicy:              "read-only",
		SupportedModes:           []bench.BenchmarkMode{bench.ModeFullVerk, bench.ModeWorkerOnly},
	}
}
