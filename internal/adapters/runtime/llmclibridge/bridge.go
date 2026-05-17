// Package llmclibridge adapts Verk runtime requests to Fabrikk llmclient
// backends while keeping Verk's user-facing runtime names stable.
package llmclibridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
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

type RuntimeDiagnostic struct {
	Runtime   string
	Available bool
	Details   string
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
		return Result{}, sanitizeRuntimeError(req.RuntimeName, err)
	}

	result := collectEvents(events, req.OnProgress)
	result.Stdout = capture.Stdout()
	result.Stderr = append(capture.Stderr(), result.Stderr...)
	result.BackendName = backend.Name()
	return result, nil
}

func (b *Bridge) CheckAvailability(ctx context.Context, runtimeName, command string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := ResolveBackendConfig(runtimeName, command)
	if err != nil {
		return err
	}

	probe := exec.CommandContext(ctx, cfg.Path, "--version")
	probe.Env = b.baseEnv()
	if output, err := probe.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s unavailable: %s", runtimeName, detail)
	}
	return nil
}

func (b *Bridge) DiagnoseRuntime(ctx context.Context, runtimeName, command string) RuntimeDiagnostic {
	if ctx == nil {
		ctx = context.Background()
	}
	runtimeName = strings.TrimSpace(runtimeName)
	diag := RuntimeDiagnostic{Runtime: runtimeName}

	cfg, err := ResolveBackendConfig(runtimeName, command)
	if err != nil {
		diag.Details = resolveDiagnosticDetail(runtimeName, command, err)
		return diag
	}
	diag.Runtime = cfg.RuntimeName

	backend, err := b.newBackend(cfg)
	if err != nil {
		diag.Details = sanitizeRuntimeDetail(cfg.RuntimeName, err)
		return diag
	}
	defer func() { _ = backend.Close() }()

	if err := checkRequiredRuntimeOptions(cfg.RuntimeName, backend); err != nil {
		diag.Details = err.Error()
		return diag
	}

	ready := backend.Ready(ctx)
	if ready.State != llmclient.ReadyOK {
		diag.Details = readinessDiagnosticDetail(cfg.RuntimeName, ready)
		return diag
	}

	diag.Available = true
	diag.Details = "ready"
	return diag
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

type capabilitiesBackend interface {
	Capabilities() llmclient.Capabilities
}

func checkRequiredRuntimeOptions(runtimeName string, backend llmclient.Backend) error {
	provider, ok := backend.(capabilitiesBackend)
	if !ok {
		return nil
	}

	caps := provider.Capabilities()
	unsupported := make([]llmclient.OptionName, 0)
	for _, name := range requiredRuntimeOptions(runtimeName) {
		if caps.OptionSupport[name] == llmclient.OptionSupportNone || caps.OptionSupport[name] == "" {
			unsupported = append(unsupported, name)
		}
	}
	if len(unsupported) == 0 {
		return nil
	}
	return &runtimeUnsupportedOptionError{runtimeName: runtimeName, options: unsupported}
}

func requiredRuntimeOptions(runtimeName string) []llmclient.OptionName {
	opts := []llmclient.OptionName{
		llmclient.OptionWorkingDirectory,
		llmclient.OptionEnvironment,
		llmclient.OptionTimeout,
		llmclient.OptionRawCapture,
	}
	if strings.TrimSpace(runtimeName) == RuntimeCodex {
		opts = append(opts,
			llmclient.OptionCodexJSONL,
		)
	}
	return opts
}

type runtimeUnsupportedOptionError struct {
	runtimeName string
	options     []llmclient.OptionName
}

func (e *runtimeUnsupportedOptionError) Error() string {
	return fmt.Sprintf("%s runtime does not support required options: %s", e.runtimeName, optionNames(e.options))
}

func (e *runtimeUnsupportedOptionError) Is(target error) bool {
	return target == llmclient.ErrUnsupportedOption
}

func (e *runtimeUnsupportedOptionError) Unwrap() error {
	return llmclient.ErrUnsupportedOption
}

func sanitizeRuntimeError(runtimeName string, err error) error {
	var unsupported *llmclient.UnsupportedOptionError
	if errors.As(err, &unsupported) {
		return &runtimeUnsupportedOptionError{
			runtimeName: strings.TrimSpace(runtimeName),
			options:     unsupported.Options,
		}
	}
	return err
}

func sanitizeRuntimeDetail(runtimeName string, err error) string {
	if err == nil {
		return ""
	}
	return sanitizeRuntimeDetailString(runtimeName, err.Error())
}

func sanitizeRuntimeDetailString(runtimeName, detail string) string {
	return strings.ReplaceAll(detail, backendCodexExec, strings.TrimSpace(runtimeName))
}

func resolveDiagnosticDetail(runtimeName, command string, err error) string {
	if err == nil {
		return ""
	}
	runtimeName = strings.TrimSpace(runtimeName)
	if runtimeName == "" {
		return err.Error()
	}
	command = strings.TrimSpace(command)
	if command == "" {
		command = runtimeName
	}
	var pathErr *exec.Error
	if errors.As(err, &pathErr) {
		return fmt.Sprintf("%s binary %q not found in PATH; install %s or configure the runtime command", runtimeName, pathErr.Name, runtimeName)
	}
	if os.IsNotExist(err) {
		return fmt.Sprintf("%s binary %q not found; install %s or configure the runtime command", runtimeName, command, runtimeName)
	}
	return sanitizeRuntimeDetail(runtimeName, err)
}

func readinessDiagnosticDetail(runtimeName string, report llmclient.ReadyReport) string {
	detail := strings.TrimSpace(report.Detail)
	switch report.State {
	case llmclient.ReadyMissingBinary:
		if detail == "" {
			return fmt.Sprintf("%s binary missing; install %s or configure the runtime command", runtimeName, runtimeName)
		}
		return fmt.Sprintf("%s binary missing: %s; install %s or configure the runtime command", runtimeName, sanitizeRuntimeDetailString(runtimeName, detail), runtimeName)
	case llmclient.ReadyNotAuthed:
		if detail == "" {
			return fmt.Sprintf("%s not authenticated; run the %s CLI login flow", runtimeName, runtimeName)
		}
		return fmt.Sprintf("%s not authenticated: %s", runtimeName, sanitizeRuntimeDetailString(runtimeName, detail))
	case llmclient.ReadyUnknown:
		if detail == "" {
			return fmt.Sprintf("%s readiness could not be determined", runtimeName)
		}
		return fmt.Sprintf("%s readiness could not be determined: %s", runtimeName, sanitizeRuntimeDetailString(runtimeName, detail))
	default:
		if detail == "" {
			return fmt.Sprintf("%s readiness failed", runtimeName)
		}
		return fmt.Sprintf("%s readiness failed: %s", runtimeName, sanitizeRuntimeDetailString(runtimeName, detail))
	}
}

func optionNames(options []llmclient.OptionName) string {
	names := make([]string, 0, len(options))
	for _, option := range options {
		names = append(names, string(option))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
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
	summary := summarizeToolCall(call)
	if summary == "" {
		onProgress(prefix)
		return
	}
	onProgress(summary)
}

func summarizeToolCall(call *llmclient.ToolCall) string {
	if call == nil {
		return ""
	}
	name := strings.TrimSpace(call.Name)
	if name == "" {
		return ""
	}
	params := call.Arguments
	switch name {
	case "Read":
		if fp, ok := stringArg(params, "file_path"); ok {
			return fmt.Sprintf("reading %s", shortenPath(fp))
		}
	case "Write":
		if fp, ok := stringArg(params, "file_path"); ok {
			return fmt.Sprintf("writing %s", shortenPath(fp))
		}
	case "Edit":
		if fp, ok := stringArg(params, "file_path"); ok {
			return fmt.Sprintf("editing %s", shortenPath(fp))
		}
	case "Bash":
		if cmd, ok := stringArg(params, "command"); ok {
			if len(cmd) > 50 {
				cmd = cmd[:47] + "..."
			}
			return fmt.Sprintf("$ %s", cmd)
		}
	case "Glob":
		if pattern, ok := stringArg(params, "pattern"); ok {
			return fmt.Sprintf("searching %s", pattern)
		}
	case "Grep":
		if pattern, ok := stringArg(params, "pattern"); ok {
			return fmt.Sprintf("grep %s", pattern)
		}
	}
	return name
}

func stringArg(params map[string]interface{}, key string) (string, bool) {
	if params == nil {
		return "", false
	}
	value, ok := params[key].(string)
	if !ok || value == "" {
		return "", false
	}
	return value, true
}

func shortenPath(path string) string {
	if idx := strings.Index(path, "/internal/"); idx >= 0 {
		return path[idx+1:]
	}
	if idx := strings.Index(path, "/cmd/"); idx >= 0 {
		return path[idx+1:]
	}
	if idx := strings.Index(path, "/pkg/"); idx >= 0 {
		return path[idx+1:]
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
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
