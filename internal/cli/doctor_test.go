package cli

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"verk/internal/engine"
)

func TestDoctorCmdRendersUserFacingRuntimeNamesWithoutBackendName(t *testing.T) {
	orig := runDoctor
	t.Cleanup(func() { runDoctor = orig })

	runDoctor = func(string) (engine.DoctorReport, int, error) {
		return doctorRuntimeFixture(), 1, nil
	}

	stdout, stderr := osPipe(t)
	exitCode := ExecuteArgs([]string{"doctor"}, stdout, stderr)
	if exitCode != 1 {
		t.Fatalf("expected doctor exit code 1, got %d", exitCode)
	}

	_ = stdout.Sync()
	data, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "Runtime claude") {
		t.Fatalf("expected claude runtime in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Runtime codex") {
		t.Fatalf("expected codex runtime in output, got:\n%s", output)
	}
	for _, want := range []string{
		"Install Claude Code CLI and run `claude --help`.",
		"Install Codex CLI and confirm `codex --help` succeeds.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected actionable detail %q, got:\n%s", want, output)
		}
	}
	if strings.Contains(output, "codex-exec") {
		t.Fatalf("doctor output leaked backend name:\n%s", output)
	}

	_ = stderr.Sync()
}

func TestDoctorCmdJSONUsesUserFacingRuntimeNamesWithoutBackendName(t *testing.T) {
	orig := runDoctor
	t.Cleanup(func() { runDoctor = orig })

	runDoctor = func(string) (engine.DoctorReport, int, error) {
		return doctorRuntimeFixture(), 1, nil
	}

	stdout, stderr := osPipe(t)
	exitCode := ExecuteArgs([]string{"doctor", "--json"}, stdout, stderr)
	if exitCode != 1 {
		t.Fatalf("expected doctor exit code 1, got %d", exitCode)
	}

	_ = stdout.Sync()
	data, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "codex-exec") {
		t.Fatalf("doctor JSON leaked backend name:\n%s", output)
	}

	var report engine.DoctorReport
	if err := json.NewDecoder(strings.NewReader(output)).Decode(&report); err != nil {
		t.Fatalf("unmarshal doctor JSON: %v\n%s", err, output)
	}

	runtimes := make([]string, 0, len(report.Runtimes))
	for _, rt := range report.Runtimes {
		runtimes = append(runtimes, rt.Runtime)
		if rt.Details == "" || !strings.Contains(rt.Details, "Install ") {
			t.Fatalf("expected actionable detail for runtime %q, got %q", rt.Runtime, rt.Details)
		}
		if strings.Contains(rt.Details, "codex-exec") {
			t.Fatalf("runtime %q detail leaked backend name: %q", rt.Runtime, rt.Details)
		}
	}
	if want := []string{"claude", "codex"}; !reflect.DeepEqual(runtimes, want) {
		t.Fatalf("expected runtime names %v, got %v", want, runtimes)
	}

	_ = stderr.Sync()
}

func doctorRuntimeFixture() engine.DoctorReport {
	return engine.DoctorReport{
		RepoRoot: "/repo",
		Checks: []engine.DoctorCheck{
			{Name: "repo_root", Status: "passed", Details: "/repo"},
		},
		Runtimes: []engine.RuntimeCheck{
			{
				Runtime:   "claude",
				Available: false,
				Details:   "Install Claude Code CLI and run `claude --help`.",
			},
			{
				Runtime:   "codex",
				Available: false,
				Details:   "Install Codex CLI and confirm `codex --help` succeeds.",
			},
		},
	}
}
