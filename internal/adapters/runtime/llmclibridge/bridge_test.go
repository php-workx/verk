package llmclibridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/php-workx/fabrikk/llmclient"
)

func TestBackendForRuntime(t *testing.T) {
	t.Parallel()

	backend, binary, err := BackendForRuntime(RuntimeCodex)
	if err != nil {
		t.Fatalf("BackendForRuntime(codex) returned error: %v", err)
	}
	if backend != backendCodexExec || binary != RuntimeCodex {
		t.Fatalf("codex mapped to backend=%q binary=%q", backend, binary)
	}

	backend, binary, err = BackendForRuntime(RuntimeClaude)
	if err != nil {
		t.Fatalf("BackendForRuntime(claude) returned error: %v", err)
	}
	if backend != backendClaude || binary != RuntimeClaude {
		t.Fatalf("claude mapped to backend=%q binary=%q", backend, binary)
	}

	if _, _, err := BackendForRuntime("gemini"); err == nil {
		t.Fatal("expected unsupported runtime error")
	}
}

func TestResolveBackendConfigResolvesBareCustomCommand(t *testing.T) {
	dir := t.TempDir()
	exe := writeExecutable(t, dir, "my-codex")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg, err := ResolveBackendConfig(RuntimeCodex, "my-codex")
	if err != nil {
		t.Fatalf("ResolveBackendConfig returned error: %v", err)
	}
	if cfg.BackendName != backendCodexExec {
		t.Fatalf("expected codex-exec backend, got %q", cfg.BackendName)
	}
	if cfg.Binary != RuntimeCodex {
		t.Fatalf("expected canonical codex binary, got %q", cfg.Binary)
	}
	if cfg.Path != exe {
		t.Fatalf("expected resolved path %q, got %q", exe, cfg.Path)
	}
}

func TestRunBuildsContextOptionsAndRawCapture(t *testing.T) {
	t.Parallel()

	exe := writeExecutable(t, t.TempDir(), "codex")
	worktree := t.TempDir()
	fake := &fakeBackend{
		name: backendCodexExec,
		events: []llmclient.Event{
			{Type: llmclient.EventStart, Fidelity: &llmclient.Fidelity{
				OptionResults: map[llmclient.OptionName]llmclient.OptionResult{
					llmclient.OptionCodexJSONL: llmclient.OptionApplied,
				},
			}},
			{Type: llmclient.EventTextDelta, Delta: "hello "},
			{Type: llmclient.EventTextDelta, Delta: "world"},
			{Type: llmclient.EventDone, Message: textMessage("ignored duplicate"), Usage: &llmclient.Usage{InputTokens: 10, OutputTokens: 3}},
		},
		captureStdout: []byte(`{"event":"turn.completed"}` + "\n"),
		captureStderr: []byte("stderr chunk"),
	}
	bridge := New(
		WithBackendFactory(func(cfg BackendConfig) (llmclient.Backend, error) {
			if cfg.BackendName != backendCodexExec {
				t.Fatalf("expected codex-exec backend, got %q", cfg.BackendName)
			}
			return fake, nil
		}),
		WithBaseEnv(func() []string { return []string{"PATH=/usr/bin", "KEEP=1"} }),
	)

	result, err := bridge.Run(context.Background(), Request{
		RuntimeName:  RuntimeCodex,
		Command:      exe,
		SystemPrompt: "system",
		UserPrompt:   "prompt",
		Model:        "gpt-test",
		Reasoning:    "high",
		WorktreePath: worktree,
		Timeout:      2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Text != "hello world" {
		t.Fatalf("expected delta text without done duplication, got %q", result.Text)
	}
	if string(result.Stdout) != string(fake.captureStdout) {
		t.Fatalf("expected raw stdout capture, got %q", result.Stdout)
	}
	if string(result.Stderr) != string(fake.captureStderr) {
		t.Fatalf("expected raw stderr capture, got %q", result.Stderr)
	}
	if result.BackendName != backendCodexExec {
		t.Fatalf("expected backend name %q, got %q", backendCodexExec, result.BackendName)
	}
	if result.Usage == nil || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 3 {
		t.Fatalf("expected usage from done event, got %#v", result.Usage)
	}

	if fake.input.SystemPrompt != "system" {
		t.Fatalf("expected system prompt to be propagated, got %q", fake.input.SystemPrompt)
	}
	if got := fake.userPrompt(); got != "prompt" {
		t.Fatalf("expected user prompt, got %q", got)
	}
	assertOption(t, fake.config.Model, "gpt-test", "model")
	assertOption(t, fake.config.WorkingDirectory, worktree, "working directory")
	assertOption(t, fake.config.Timeout, 2*time.Minute, "timeout")
	assertOption(t, fake.config.ReasoningEffort, "high", "reasoning")
	if !fake.config.EnvironmentSet || !containsEnv(fake.config.Environment, "KEEP=1") {
		t.Fatalf("expected environment replacement with KEEP=1, got %#v", fake.config.Environment)
	}
	if !fake.config.CodexJSONL {
		t.Fatal("expected Codex JSONL mode")
	}
	for _, name := range []llmclient.OptionName{
		llmclient.OptionWorkingDirectory,
		llmclient.OptionEnvironment,
		llmclient.OptionTimeout,
		llmclient.OptionRawCapture,
		llmclient.OptionCodexJSONL,
		llmclient.OptionReasoningEffort,
	} {
		if _, ok := fake.config.RequiredOptions[name]; !ok {
			t.Fatalf("expected required option %s in %#v", name, fake.config.RequiredOptions)
		}
	}
}

func TestRunTextFallbacks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events []llmclient.Event
		want   string
	}{
		{
			name: "text end without deltas",
			events: []llmclient.Event{
				{Type: llmclient.EventTextEnd, Content: "from text end"},
				{Type: llmclient.EventDone, Message: textMessage("ignored")},
			},
			want: "from text end",
		},
		{
			name: "done message without text events",
			events: []llmclient.Event{
				{Type: llmclient.EventDone, Message: textMessage("from done message")},
			},
			want: "from done message",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeBackend{name: backendClaude, events: tt.events}
			bridge := New(
				WithBackendFactory(func(BackendConfig) (llmclient.Backend, error) { return fake, nil }),
				WithBaseEnv(func() []string { return []string{"PATH=/usr/bin"} }),
			)
			result, err := bridge.Run(context.Background(), Request{
				RuntimeName: RuntimeClaude,
				Command:     writeExecutable(t, t.TempDir(), "claude"),
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if result.Text != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, result.Text)
			}
		})
	}
}

func TestRunErrorAndCancelledEventsSetNonZeroExit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		events     []llmclient.Event
		wantStderr string
	}{
		{
			name:       "error event",
			events:     []llmclient.Event{{Type: llmclient.EventError, ErrorMessage: "backend failed"}},
			wantStderr: "backend failed",
		},
		{
			name:       "cancelled done event",
			events:     []llmclient.Event{{Type: llmclient.EventDone, Reason: llmclient.StopCancelled}},
			wantStderr: "",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeBackend{name: backendClaude, events: tt.events}
			bridge := New(
				WithBackendFactory(func(BackendConfig) (llmclient.Backend, error) { return fake, nil }),
				WithBaseEnv(func() []string { return []string{"PATH=/usr/bin"} }),
			)
			result, err := bridge.Run(context.Background(), Request{
				RuntimeName: RuntimeClaude,
				Command:     writeExecutable(t, t.TempDir(), "claude"),
			})
			if err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			if result.ExitCode == 0 {
				t.Fatal("expected non-zero exit code")
			}
			if !strings.Contains(string(result.Stderr), tt.wantStderr) {
				t.Fatalf("expected stderr to contain %q, got %q", tt.wantStderr, result.Stderr)
			}
		})
	}
}

func TestRunProgressFromToolCallEvents(t *testing.T) {
	t.Parallel()

	fake := &fakeBackend{
		name: backendClaude,
		events: []llmclient.Event{
			{Type: llmclient.EventToolCallStart, ToolCall: &llmclient.ToolCall{Name: "Read"}},
			{Type: llmclient.EventToolCallEnd, ToolCall: &llmclient.ToolCall{Name: "Read"}},
		},
	}
	bridge := New(
		WithBackendFactory(func(BackendConfig) (llmclient.Backend, error) { return fake, nil }),
		WithBaseEnv(func() []string { return []string{"PATH=/usr/bin"} }),
	)
	var progress []string
	_, err := bridge.Run(context.Background(), Request{
		RuntimeName: RuntimeClaude,
		Command:     writeExecutable(t, t.TempDir(), "claude"),
		OnProgress:  func(detail string) { progress = append(progress, detail) },
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	want := []string{"tool call started: Read", "tool call finished: Read"}
	if !reflect.DeepEqual(progress, want) {
		t.Fatalf("expected progress %#v, got %#v", want, progress)
	}
}

func TestRunReturnsStreamErrorBeforeExecution(t *testing.T) {
	t.Parallel()

	streamErr := errors.New("unsupported required option")
	fake := &fakeBackend{name: backendClaude, streamErr: streamErr}
	bridge := New(
		WithBackendFactory(func(BackendConfig) (llmclient.Backend, error) { return fake, nil }),
		WithBaseEnv(func() []string { return []string{"PATH=/usr/bin"} }),
	)
	_, err := bridge.Run(context.Background(), Request{
		RuntimeName: RuntimeClaude,
		Command:     writeExecutable(t, t.TempDir(), "claude"),
	})
	if !errors.Is(err, streamErr) {
		t.Fatalf("expected stream error %v, got %v", streamErr, err)
	}
}

type fakeBackend struct {
	name          string
	events        []llmclient.Event
	streamErr     error
	captureStdout []byte
	captureStderr []byte
	input         *llmclient.Context
	config        llmclient.RequestConfig
}

func (b *fakeBackend) Stream(_ context.Context, input *llmclient.Context, opts ...llmclient.Option) (<-chan llmclient.Event, error) {
	b.input = input
	b.config = llmclient.ApplyOptions(llmclient.DefaultRequestConfig(), opts)
	if b.streamErr != nil {
		return nil, b.streamErr
	}
	if b.config.RawCapture != nil {
		b.config.RawCapture(llmclient.RawStreamStdout, b.captureStdout)
		b.config.RawCapture(llmclient.RawStreamStderr, b.captureStderr)
	}
	ch := make(chan llmclient.Event, len(b.events))
	for _, event := range b.events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (b *fakeBackend) Name() string { return b.name }

func (b *fakeBackend) Available() bool { return true }

func (b *fakeBackend) Ready(context.Context) llmclient.ReadyReport {
	return llmclient.ReadyReport{State: llmclient.ReadyOK}
}

func (b *fakeBackend) Close() error { return nil }

func (b *fakeBackend) userPrompt() string {
	if b.input == nil || len(b.input.Messages) == 0 || len(b.input.Messages[0].Content) == 0 {
		return ""
	}
	return b.input.Messages[0].Content[0].Text
}

func textMessage(text string) *llmclient.AssistantMessage {
	return &llmclient.AssistantMessage{
		Content: []llmclient.ContentBlock{{Type: llmclient.ContentText, Text: text}},
	}
}

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}

func assertOption[T comparable](t *testing.T, got, want T, name string) {
	t.Helper()

	if got != want {
		t.Fatalf("expected %s %#v, got %#v", name, want, got)
	}
}

func containsEnv(env []string, pair string) bool {
	for _, got := range env {
		if got == pair {
			return true
		}
	}
	return false
}
