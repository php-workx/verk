// Package llmclibridge adapts Verk runtime requests to Fabrikk llmclient
// backends while keeping Verk's user-facing runtime names stable.
package llmclibridge

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	verkruntime "verk/internal/adapters/runtime"

	"github.com/php-workx/fabrikk/llmcli"
	"github.com/php-workx/fabrikk/llmclient"
)

const (
	RuntimeClaude = "claude"
	RuntimeCodex  = "codex"
)

const (
	backendClaude    = "claude"
	backendCodexExec = "codex-exec"
)

type BackendConfig struct {
	RuntimeName string
	BackendName string
	Binary      string
	Path        string
}

type BackendFactory func(BackendConfig) (llmclient.Backend, error)

type Bridge struct {
	newBackend BackendFactory
	baseEnv    func() []string
}

type Option func(*Bridge)

func WithBackendFactory(factory BackendFactory) Option {
	return func(b *Bridge) {
		if factory != nil {
			b.newBackend = factory
		}
	}
}

func WithBaseEnv(fn func() []string) Option {
	return func(b *Bridge) {
		if fn != nil {
			b.baseEnv = fn
		}
	}
}

func New(opts ...Option) *Bridge {
	b := &Bridge{
		newBackend: NewBackend,
		baseEnv: func() []string {
			return verkruntime.StripEnvKeys(os.Environ(), verkruntime.GitIsolationKeys()...)
		},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(b)
		}
	}
	return b
}

type Request struct {
	RuntimeName  string
	Command      string
	SystemPrompt string
	UserPrompt   string
	Model        string
	Reasoning    string
	WorktreePath string
	Timeout      time.Duration
	OnProgress   func(detail string)
}

type Result struct {
	Stdout      []byte
	Stderr      []byte
	Text        string
	ExitCode    int
	BackendName string
	Fidelity    *llmclient.Fidelity
	Usage       *llmclient.Usage
}

func NewBackend(cfg BackendConfig) (llmclient.Backend, error) {
	if strings.TrimSpace(cfg.BackendName) == "" {
		return nil, fmt.Errorf("llmcli backend name is empty")
	}
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, fmt.Errorf("llmcli backend path is empty")
	}
	return llmcli.NewBackendByName(cfg.BackendName, llmcli.CliInfo{
		Name:   cfg.RuntimeName,
		Binary: cfg.Binary,
		Path:   cfg.Path,
	})
}

func BackendForRuntime(runtimeName string) (backendName, binary string, err error) {
	switch strings.TrimSpace(runtimeName) {
	case RuntimeClaude:
		return backendClaude, RuntimeClaude, nil
	case RuntimeCodex:
		return backendCodexExec, RuntimeCodex, nil
	default:
		return "", "", fmt.Errorf("unsupported runtime %q", runtimeName)
	}
}

func ResolveBackendConfig(runtimeName, command string) (BackendConfig, error) {
	backendName, binary, err := BackendForRuntime(runtimeName)
	if err != nil {
		return BackendConfig{}, err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		command = binary
	}
	executable, err := verkruntime.ValidatedExecutable(command)
	if err != nil {
		return BackendConfig{}, err
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return BackendConfig{}, fmt.Errorf("resolve %s executable %q: %w", runtimeName, executable, err)
	}
	return BackendConfig{
		RuntimeName: strings.TrimSpace(runtimeName),
		BackendName: backendName,
		Binary:      binary,
		Path:        path,
	}, nil
}

func (b *Bridge) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := ResolveBackendConfig(req.RuntimeName, req.Command)
	if err != nil {
		return Result{}, err
	}
	backend, err := b.newBackend(cfg)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = backend.Close() }()

	env, err := verkruntime.BuildIsolatedProcessEnv(b.baseEnv(), req.WorktreePath)
	if err != nil {
		return Result{}, fmt.Errorf("build %s runtime environment: %w", req.RuntimeName, err)
	}

	capture := &rawCapture{}
	opts := requestOptions(req, env, capture.Append)
	events, err := backend.Stream(ctx, requestContext(req), opts...)
	if err != nil {
		return Result{}, err
	}

	result := collectEvents(events, req.OnProgress)
	result.Stdout = capture.Stdout()
	result.Stderr = append(capture.Stderr(), result.Stderr...)
	result.BackendName = backend.Name()
	return result, nil
}

func requestContext(req Request) *llmclient.Context {
	return &llmclient.Context{
		SystemPrompt: req.SystemPrompt,
		Messages: []llmclient.Message{
			{
				Role: llmclient.RoleUser,
				Content: []llmclient.ContentBlock{
					{Type: llmclient.ContentText, Text: req.UserPrompt},
				},
			},
		},
	}
}

func requestOptions(req Request, env []string, capture llmclient.RawCaptureFunc) []llmclient.Option {
	opts := []llmclient.Option{
		llmclient.WithWorkingDirectory(req.WorktreePath),
		llmclient.WithEnvironment(env),
		llmclient.WithTimeout(req.Timeout),
		llmclient.WithRawCapture(capture),
		llmclient.WithRequiredOptions(
			llmclient.OptionWorkingDirectory,
			llmclient.OptionEnvironment,
			llmclient.OptionTimeout,
			llmclient.OptionRawCapture,
		),
	}
	if strings.TrimSpace(req.Model) != "" {
		opts = append(opts, llmclient.WithModel(req.Model))
	}
	if strings.TrimSpace(req.RuntimeName) == RuntimeCodex {
		opts = append(opts,
			llmclient.WithCodexJSONL(true),
			llmclient.WithRequiredOptions(llmclient.OptionCodexJSONL),
		)
		if strings.TrimSpace(req.Reasoning) != "" {
			opts = append(opts,
				llmclient.WithReasoningEffort(req.Reasoning),
				llmclient.WithRequiredOptions(llmclient.OptionReasoningEffort),
			)
		}
	}
	return opts
}

type rawCapture struct {
	mu     sync.Mutex
	stdout []byte
	stderr []byte
}

func (c *rawCapture) Append(stream llmclient.RawStream, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch stream {
	case llmclient.RawStreamStdout:
		c.stdout = append(c.stdout, data...)
	case llmclient.RawStreamStderr:
		c.stderr = append(c.stderr, data...)
	}
}

func (c *rawCapture) Stdout() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.stdout...)
}

func (c *rawCapture) Stderr() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.stderr...)
}

func collectEvents(events <-chan llmclient.Event, onProgress func(string)) Result {
	var result Result
	var deltaText strings.Builder
	var textEnd strings.Builder
	sawDelta := false

	for event := range events {
		switch event.Type {
		case llmclient.EventStart:
			result.Fidelity = event.Fidelity
		case llmclient.EventTextDelta:
			sawDelta = true
			deltaText.WriteString(event.Delta)
		case llmclient.EventTextEnd:
			if !sawDelta {
				textEnd.WriteString(event.Content)
			}
		case llmclient.EventToolCallStart:
			emitProgress(onProgress, "tool call started", event.ToolCall)
		case llmclient.EventToolCallEnd:
			emitProgress(onProgress, "tool call finished", event.ToolCall)
		case llmclient.EventDone:
			result.Usage = event.Usage
			if event.Usage == nil && event.Message != nil {
				result.Usage = event.Message.Usage
			}
			if event.Reason == llmclient.StopCancelled {
				result.ExitCode = 1
			}
			if !sawDelta && textEnd.Len() == 0 && event.Message != nil {
				textEnd.WriteString(messageText(event.Message))
			}
		case llmclient.EventError:
			result.ExitCode = 1
			result.Stderr = appendErrorMessage(result.Stderr, event.ErrorMessage)
		}
	}

	if sawDelta {
		result.Text = deltaText.String()
	} else {
		result.Text = textEnd.String()
	}
	return result
}

func emitProgress(onProgress func(string), prefix string, call *llmclient.ToolCall) {
	if onProgress == nil || call == nil {
		return
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		onProgress(prefix)
		return
	}
	onProgress(prefix + ": " + name)
}

func messageText(message *llmclient.AssistantMessage) string {
	if message == nil {
		return ""
	}
	var out strings.Builder
	for _, block := range message.Content {
		if block.Type == llmclient.ContentText {
			out.WriteString(block.Text)
		}
	}
	return out.String()
}

func appendErrorMessage(stderr []byte, message string) []byte {
	message = strings.TrimSpace(message)
	if message == "" {
		return stderr
	}
	if len(stderr) > 0 && !strings.HasSuffix(string(stderr), "\n") {
		stderr = append(stderr, '\n')
	}
	return append(stderr, []byte(message)...)
}
