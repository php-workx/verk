package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"verk/internal/policy"
)

func TestInitCmd_FirstRunDefaultsReviewerProfileFromWorkerPrompts(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	stdout, stderr, err := runInitInDir(t, dir, strings.Join([]string{
		"",             // root quality commands
		"",             // no subdirectory quality commands
		"", "", "", "", // policy defaults
		"", "", // runtime timeout defaults
		"codex",               // worker runtime
		"gpt-5.3-codex-spark", // worker model
		"high",                // worker reasoning
		"", "", "",            // reviewer defaults from worker profile
	}, "\n")+"\n")
	if err != nil {
		t.Fatalf("init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	cfg, err := policy.LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	want := policy.RoleProfile{Runtime: "codex", Model: "gpt-5.3-codex-spark", Reasoning: "high"}
	if cfg.Runtime.Worker != want {
		t.Fatalf("worker profile = %+v, want %+v", cfg.Runtime.Worker, want)
	}
	if cfg.Runtime.Reviewer != want {
		t.Fatalf("reviewer profile = %+v, want %+v", cfg.Runtime.Reviewer, want)
	}
	if cfg.Runtime.DefaultRuntime != want.Runtime {
		t.Fatalf("default runtime = %q, want %q", cfg.Runtime.DefaultRuntime, want.Runtime)
	}
	if !strings.Contains(stdout, "Reviewer runtime [codex]") {
		t.Fatalf("expected reviewer runtime prompt to default from worker, got:\n%s", stdout)
	}
}

func TestInitCmd_RepeatedRunShowsAndPreservesExistingRuntimeProfilesOnBlankInput(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	existing := policy.DefaultConfig()
	existing.Runtime.Worker = policy.RoleProfile{Runtime: "codex", Model: "gpt-5.3-codex-spark", Reasoning: "medium"}
	existing.Runtime.Reviewer = policy.RoleProfile{Runtime: "claude", Model: "opus", Reasoning: "xhigh"}
	existing.Runtime.DefaultRuntime = "codex"
	if err := policy.WriteConfig(dir, existing); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	stdout, stderr, err := runInitInDir(t, dir, strings.Repeat("\n", 14))
	if err != nil {
		t.Fatalf("init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	cfg, err := policy.LoadConfig(dir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Runtime.Worker != existing.Runtime.Worker {
		t.Fatalf("worker profile changed on blank input: got %+v want %+v", cfg.Runtime.Worker, existing.Runtime.Worker)
	}
	if cfg.Runtime.Reviewer != existing.Runtime.Reviewer {
		t.Fatalf("reviewer profile changed on blank input: got %+v want %+v", cfg.Runtime.Reviewer, existing.Runtime.Reviewer)
	}
	for _, wantPrompt := range []string{
		"Worker runtime [codex]",
		"Worker model [gpt-5.3-codex-spark]",
		"Worker reasoning [medium]",
		"Reviewer runtime [claude]",
		"Reviewer model [opus]",
		"Reviewer reasoning [xhigh]",
	} {
		if !strings.Contains(stdout, wantPrompt) {
			t.Fatalf("expected prompt %q in stdout, got:\n%s", wantPrompt, stdout)
		}
	}
}

type errReader struct {
	data []byte
	err  error
	pos  int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		if r.err == nil {
			return 0, io.EOF
		}
		return 0, r.err
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func TestInitCmd_FailingScannerPreventsConfigWrite(t *testing.T) {
	dir := t.TempDir()
	initCLITestRepo(t, dir)

	reader := &errReader{data: []byte("\n"), err: io.ErrUnexpectedEOF}
	stdout, stderr, err := runInitInDirWithReader(t, dir, reader)
	if err == nil {
		t.Fatalf("expected init to fail on read error")
	}
	if !strings.Contains(err.Error(), "read input:") {
		t.Fatalf("expected read input error, got: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected wrapped scanner read error, got: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".verk", "config.yaml")); !os.IsNotExist(statErr) {
		if statErr == nil {
			t.Fatalf("expected config file to not be written after scanner error")
		}
		t.Fatalf("unexpected stat error: %v\nstdout:\n%s\nstderr:\n%s", statErr, stdout, stderr)
	}
}

func runInitInDir(t *testing.T, dir, stdin string) (string, string, error) {
	return runInitInDirWithReader(t, dir, strings.NewReader(stdin))
}

func runInitInDirWithReader(t *testing.T, dir string, stdin io.Reader) (string, string, error) {
	t.Helper()
	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp repo: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Errorf("restore wd: %v", err)
		}
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	root := newRootCmd()
	root.SetArgs([]string{"init"})
	root.SetIn(stdin)
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err = root.Execute()
	return stdout.String(), stderr.String(), err
}
