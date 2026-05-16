package cli

import (
	"bytes"
	"strings"
	"testing"
	"verk/internal/bench"
)

// runBenchCmd invokes the bench subcommand via a fresh root command tree and
// captures stdout and stderr as strings together with the exit code.
func runBenchCmd(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	root := newRootCmd()
	root.SetArgs(append([]string{"bench"}, args...))
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	err := root.Execute()
	if err != nil {
		code = 1
		if e, ok := err.(*cliExitError); ok {
			code = e.ExitCode()
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// stubProvider is a minimal bench.Provider for use in list tests.
type stubProvider struct {
	name   string
	suites []bench.SuiteMeta
}

func (s *stubProvider) Name() string                                 { return s.name }
func (s *stubProvider) Suites() []bench.SuiteMeta                    { return s.suites }
func (s *stubProvider) LoadTasks(suite string) ([]bench.Task, error) { return nil, nil }
func (s *stubProvider) Capabilities() bench.ProviderCapabilities     { return bench.ProviderCapabilities{} }

func TestBenchList_PrintsRegisteredProviders(t *testing.T) {
	// Override the registry factory to return a registry with our stub provider.
	original := benchRegistryFactory
	t.Cleanup(func() { benchRegistryFactory = original })
	benchRegistryFactory = func() *bench.Registry {
		r := bench.NewRegistry()
		_ = r.Register(&stubProvider{
			name: "verk-native",
			suites: []bench.SuiteMeta{
				{Name: "smoke", TaskCount: 3, SamplingMode: "smoke"},
			},
		})
		return r
	}

	stdout, _, code := runBenchCmd(t, "list")
	if code != 0 {
		t.Fatalf("bench list: expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "verk-native") {
		t.Fatalf("bench list: expected 'verk-native' in output, got:\n%s", stdout)
	}
}

func TestBenchRun_NotYetImplemented(t *testing.T) {
	_, _, code := runBenchCmd(t, "run", "smoke")
	if code == 0 {
		t.Fatal("bench run: expected non-zero exit code, got 0")
	}
}

func TestBenchReport_NotYetImplemented(t *testing.T) {
	_, _, code := runBenchCmd(t, "report", "run-abc-123")
	if code == 0 {
		t.Fatal("bench report: expected non-zero exit code, got 0")
	}
}

func TestBenchCompare_RejectsBadRunIDs(t *testing.T) {
	tests := []struct {
		name      string
		baseline  string
		candidate string
	}{
		{"baseline missing prefix", "abc-123", "run-xyz-456"},
		{"candidate missing prefix", "run-abc-123", "xyz-456"},
		{"both missing prefix", "abc-123", "xyz-456"},
		{"bare words", "baseline", "candidate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, code := runBenchCmd(t, "compare", tt.baseline, tt.candidate)
			if code == 0 {
				t.Fatalf("bench compare %s %s: expected non-zero exit, got 0", tt.baseline, tt.candidate)
			}
		})
	}
}

func TestBenchCompare_NotYetImplemented(t *testing.T) {
	// Valid run-id shapes still return an error because the feature is a stub.
	_, _, code := runBenchCmd(t, "compare", "run-baseline-1000", "run-candidate-2000")
	if code == 0 {
		t.Fatal("bench compare: expected non-zero exit code for unimplemented command, got 0")
	}
}
