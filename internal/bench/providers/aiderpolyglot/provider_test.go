package aiderpolyglot

import (
	"errors"
	"testing"
	"verk/internal/bench"
)

func TestProvider_NameIsAiderPolyglot(t *testing.T) {
	p := New()
	if got := p.Name(); got != "aider-polyglot" {
		t.Errorf("Name(): got %q, want %q", got, "aider-polyglot")
	}
}

func TestProvider_LoadTasksReturnsNotConfiguredWithoutDataset(t *testing.T) {
	p := New()
	_, err := p.LoadTasks("smoke")
	if err == nil {
		t.Fatal("expected error when DatasetPath is empty, got nil")
	}
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected ErrNotConfigured, got: %v", err)
	}
}

func TestProvider_LoadTasksWithDatasetPathReturnsNotImplemented(t *testing.T) {
	p := New()
	p.DatasetPath = "/some/path"
	_, err := p.LoadTasks("smoke")
	if err == nil {
		t.Fatal("expected error when dataset loading is not implemented, got nil")
	}
	// Should NOT be ErrNotConfigured — DatasetPath is set.
	if errors.Is(err, ErrNotConfigured) {
		t.Errorf("expected a different error (not ErrNotConfigured) when DatasetPath is set")
	}
}

func TestProvider_SuitesAdvertiseSmoke(t *testing.T) {
	p := New()
	suites := p.Suites()
	if len(suites) == 0 {
		t.Fatal("expected at least one suite, got none")
	}

	var found bool
	for _, s := range suites {
		if s.Name == "smoke" {
			found = true
			if s.SamplingMode != bench.SamplingModeSmoke {
				t.Errorf("smoke suite: SamplingMode=%q, want %q", s.SamplingMode, bench.SamplingModeSmoke)
			}
			if s.Provider != "aider-polyglot" {
				t.Errorf("smoke suite: Provider=%q, want %q", s.Provider, "aider-polyglot")
			}
			break
		}
	}
	if !found {
		t.Error("expected a suite named 'smoke', not found")
	}
}

func TestProvider_CapabilitiesAreCorrect(t *testing.T) {
	p := New()
	caps := p.Capabilities()

	if !caps.SupportsIsolatedVerifier {
		t.Error("expected SupportsIsolatedVerifier=true")
	}
	if caps.CachePolicy != "read-only" {
		t.Errorf("CachePolicy: got %q, want %q", caps.CachePolicy, "read-only")
	}

	wantModes := []bench.BenchmarkMode{bench.ModeFullVerk, bench.ModeWorkerOnly}
	if len(caps.SupportedModes) != len(wantModes) {
		t.Errorf("SupportedModes: got %v, want %v", caps.SupportedModes, wantModes)
	}
}
