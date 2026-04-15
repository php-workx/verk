package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"verk/internal/adapters/runtime"
)

const (
	runtimeName   = "claude"
	defaultBinary = "claude"
)

var (
	runCommand          = defaultRunCommand
	runStreamingCommand = defaultRunStreamingCommand
	now                 = time.Now
)

// runtimeEnvPassthrough lists additional env vars from config that should be
// explicitly set. The subprocess inherits the full parent environment so that
// Claude Code can access auth credentials (keychain, ~/.claude/, etc.).
// Config-specified AuthEnvVars are verified to exist and passed through.

type Adapter struct {
	Command string
}

type commandResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type workerArtifact struct {
	Runtime            string                   `json:"runtime"`
	Request            runtime.WorkerRequest    `json:"request"`
	CLIOutput          json.RawMessage          `json:"cli_output"`
	ResultBlock        *runtime.VerkResultBlock `json:"result_block,omitempty"`
	Normalized         runtime.WorkerResult     `json:"normalized"`
	CapturedStdoutPath string                   `json:"captured_stdout_path"`
	CapturedStderrPath string                   `json:"captured_stderr_path"`
}

type reviewArtifact struct {
	Runtime            string                   `json:"runtime"`
	Request            runtime.ReviewRequest    `json:"request"`
	CLIOutput          json.RawMessage          `json:"cli_output"`
	ReviewBlock        *runtime.VerkReviewBlock `json:"review_block,omitempty"`
	Normalized         runtime.ReviewResult     `json:"normalized"`
	CapturedStdoutPath string                   `json:"captured_stdout_path"`
	CapturedStderrPath string                   `json:"captured_stderr_path"`
}

func New() *Adapter {
	return &Adapter{Command: defaultBinary}
}

func NewWithCommand(command string) *Adapter {
	command = strings.TrimSpace(command)
	if command == "" {
		command = defaultBinary
	}
	return &Adapter{Command: command}
}

func (a *Adapter) CheckAvailability(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	result, err := runCommand(ctx, a.binary(), []string{"--version"}, nil, runtimeCommandEnv(runtime.ExecutionConfig{}), 0)
	if err != nil {
		return fmt.Errorf("%s availability check failed: %w", runtimeName, err)
	}
	if result.exitCode != 0 {
		return fmt.Errorf("%s unavailable: exit code %d: %s", runtimeName, result.exitCode, strings.TrimSpace(string(result.stderr)))
	}
	return nil
}

func (a *Adapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if err := validateWorkerRequest(req); err != nil {
		return runtime.WorkerResult{}, err
	}

	req.Runtime = ensureRuntime(req.Runtime, runtimeName)
	startedAt := now().UTC()

	var err error
	prompt := runtime.BuildWorkerPrompt(req)
	args := buildWorkerArgs(req)
	var execResult commandResult
	var execErr error
	if req.OnProgress != nil {
		execResult, execErr = runStreamingCommand(ctx, a.binary(), args, []byte(prompt), runtimeCommandEnv(req.ExecutionConfig), runtimeCommandTimeout(req.ExecutionConfig.WorkerTimeoutMinutes), req.OnProgress)
	} else {
		execResult, execErr = runCommand(ctx, a.binary(), args, []byte(prompt), runtimeCommandEnv(req.ExecutionConfig), runtimeCommandTimeout(req.ExecutionConfig.WorkerTimeoutMinutes))
	}
	finishedAt := now().UTC()
	if execErr != nil {
		return runtime.WorkerResult{}, fmt.Errorf("run %s worker: %w", runtimeName, execErr)
	}

	resultText, cliOK := runtime.ExtractCLIResultText(execResult.stdout)
	resultBlock, blockFound := runtime.ParseResultBlock(resultText)

	result := runtime.WorkerResult{
		Status:         deriveWorkerStatus(resultBlock, blockFound, cliOK, execResult.exitCode, execResult.stderr),
		CompletionCode: deriveWorkerCompletionCode(resultBlock, blockFound, execResult.exitCode),
		Concerns:       deriveWorkerConcerns(resultBlock, blockFound),
		BlockReason:    deriveWorkerBlockReason(resultBlock, blockFound, resultText, execResult.exitCode),
		RetryClass:     deriveWorkerRetryClass(resultBlock, blockFound, cliOK, execResult.exitCode, execResult.stderr),
		LeaseID:        req.LeaseID,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
	}

	result.StdoutPath, err = writeBytesArtifact(runtimeName+"-worker-stdout", execResult.stdout)
	if err != nil {
		return runtime.WorkerResult{}, err
	}
	result.StderrPath, err = writeBytesArtifact(runtimeName+"-worker-stderr", execResult.stderr)
	if err != nil {
		return runtime.WorkerResult{}, err
	}

	var blockPtr *runtime.VerkResultBlock
	if blockFound {
		blockPtr = &resultBlock
	}
	artifact := workerArtifact{
		Runtime:            runtimeName,
		Request:            req,
		CLIOutput:          safeRawJSON(execResult.stdout),
		ResultBlock:        blockPtr,
		Normalized:         result,
		CapturedStdoutPath: result.StdoutPath,
		CapturedStderrPath: result.StderrPath,
	}
	result.ResultArtifactPath, err = writeJSONArtifact(runtimeName+"-worker-result", artifact)
	if err != nil {
		return runtime.WorkerResult{}, err
	}
	artifact.Normalized.ResultArtifactPath = result.ResultArtifactPath
	if err := rewriteJSONArtifact(result.ResultArtifactPath, artifact); err != nil {
		return runtime.WorkerResult{}, err
	}

	if err := result.Validate(); err != nil {
		return runtime.WorkerResult{}, err
	}
	return result, nil
}

func (a *Adapter) RunReviewer(ctx context.Context, req runtime.ReviewRequest) (runtime.ReviewResult, error) {
	if err := validateReviewRequest(req); err != nil {
		return runtime.ReviewResult{}, err
	}

	req.Runtime = ensureRuntime(req.Runtime, runtimeName)
	startedAt := now().UTC()

	var err error
	prompt := runtime.BuildReviewPrompt(req)
	args := buildReviewArgs(req)
	var execResult commandResult
	var execErr error
	if req.OnProgress != nil {
		execResult, execErr = runStreamingCommand(ctx, a.binary(), args, []byte(prompt), runtimeCommandEnv(req.ExecutionConfig), runtimeCommandTimeout(req.ExecutionConfig.ReviewerTimeoutMinutes), req.OnProgress)
	} else {
		execResult, execErr = runCommand(ctx, a.binary(), args, []byte(prompt), runtimeCommandEnv(req.ExecutionConfig), runtimeCommandTimeout(req.ExecutionConfig.ReviewerTimeoutMinutes))
	}
	finishedAt := now().UTC()
	if execErr != nil {
		return runtime.ReviewResult{}, fmt.Errorf("run %s reviewer: %w", runtimeName, execErr)
	}

	resultText, cliOK := runtime.ExtractCLIResultText(execResult.stdout)
	reviewBlock, blockFound := runtime.ParseReviewBlock(resultText)

	findings, err := normalizeBlockFindings(reviewBlock, blockFound)
	if err != nil {
		return runtime.ReviewResult{}, err
	}

	normalized := runtime.ReviewResult{
		Status:         deriveReviewWorkerStatus(reviewBlock, blockFound, cliOK, findings, req.EffectiveReviewThreshold, execResult.exitCode, execResult.stderr),
		CompletionCode: deriveReviewCompletionCode(reviewBlock, blockFound, execResult.exitCode),
		RetryClass:     deriveReviewRetryClass(reviewBlock, blockFound, cliOK, execResult.exitCode, execResult.stderr),
		LeaseID:        req.LeaseID,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		ReviewStatus:   deriveReviewStatus(findings, req.EffectiveReviewThreshold),
		Summary:        extractReviewSummary(reviewBlock, blockFound),
		Findings:       findings,
	}

	if err := checkReviewStatusContradiction(reviewBlock, blockFound, normalized.ReviewStatus); err != nil {
		return runtime.ReviewResult{}, err
	}

	normalized.StdoutPath, err = writeBytesArtifact(runtimeName+"-review-stdout", execResult.stdout)
	if err != nil {
		return runtime.ReviewResult{}, err
	}
	normalized.StderrPath, err = writeBytesArtifact(runtimeName+"-review-stderr", execResult.stderr)
	if err != nil {
		return runtime.ReviewResult{}, err
	}

	var blockPtr *runtime.VerkReviewBlock
	if blockFound {
		blockPtr = &reviewBlock
	}
	artifact := reviewArtifact{
		Runtime:            runtimeName,
		Request:            req,
		CLIOutput:          safeRawJSON(execResult.stdout),
		ReviewBlock:        blockPtr,
		Normalized:         normalized,
		CapturedStdoutPath: normalized.StdoutPath,
		CapturedStderrPath: normalized.StderrPath,
	}
	normalized.ResultArtifactPath, err = writeJSONArtifact(runtimeName+"-review-result", artifact)
	if err != nil {
		return runtime.ReviewResult{}, err
	}
	artifact.Normalized.ResultArtifactPath = normalized.ResultArtifactPath
	if err := rewriteJSONArtifact(normalized.ResultArtifactPath, artifact); err != nil {
		return runtime.ReviewResult{}, err
	}

	if err := normalized.Validate(req.EffectiveReviewThreshold); err != nil {
		return runtime.ReviewResult{}, err
	}
	return normalized, nil
}

func (a *Adapter) binary() string {
	if strings.TrimSpace(a.Command) == "" {
		return defaultBinary
	}
	return a.Command
}

func validateWorkerRequest(req runtime.WorkerRequest) error {
	if strings.TrimSpace(req.LeaseID) == "" {
		return fmt.Errorf("worker request missing lease_id")
	}
	return nil
}

func validateReviewRequest(req runtime.ReviewRequest) error {
	if strings.TrimSpace(req.LeaseID) == "" {
		return fmt.Errorf("review request missing lease_id")
	}
	if err := runtime.ValidateSeverity(req.EffectiveReviewThreshold); err != nil {
		return err
	}
	return nil
}

// buildWorkerArgs constructs CLI args for `claude -p --output-format json`.
// The user prompt is passed via stdin.
func buildWorkerArgs(req runtime.WorkerRequest) []string {
	outputFormat := "json"
	args := []string{"-p"}
	if req.OnProgress != nil {
		outputFormat = "stream-json"
		args = append(args, "--verbose")
	}
	args = append(args,
		"--output-format", outputFormat,
		"--system-prompt", runtime.WorkerSystemPrompt(),
	)
	if req.ExecutionConfig.WorkerTimeoutMinutes > 0 {
		args = append(args, "--max-turns", "50")
	}
	return args
}

func buildReviewArgs(req runtime.ReviewRequest) []string {
	outputFormat := "json"
	args := []string{"-p"}
	if req.OnProgress != nil {
		outputFormat = "stream-json"
		args = append(args, "--verbose")
	}
	args = append(args,
		"--output-format", outputFormat,
		"--system-prompt", runtime.ReviewerSystemPrompt(),
	)
	if req.ExecutionConfig.ReviewerTimeoutMinutes > 0 {
		args = append(args, "--max-turns", "30")
	}
	return args
}

// --- Status derivation from verk protocol blocks ---

func deriveWorkerStatus(block runtime.VerkResultBlock, found, cliOK bool, exitCode int, stderr []byte) runtime.WorkerStatus {
	if found {
		if status, ok := normalizeWorkerStatusString(block.Status); ok {
			return status
		}
	}
	if cliOK && exitCode == 0 {
		return runtime.WorkerStatusDone
	}
	if looksLikeMissingContext(stderr) {
		return runtime.WorkerStatusNeedsContext
	}
	return runtime.WorkerStatusBlocked
}

func deriveWorkerCompletionCode(block runtime.VerkResultBlock, found bool, exitCode int) string {
	if found && strings.TrimSpace(block.CompletionCode) != "" {
		return strings.TrimSpace(block.CompletionCode)
	}
	if found && strings.TrimSpace(block.Status) != "" {
		return strings.TrimSpace(block.Status)
	}
	return fmt.Sprintf("exit_%d", exitCode)
}

func deriveWorkerConcerns(block runtime.VerkResultBlock, found bool) []string {
	if found && len(block.Concerns) > 0 {
		return block.Concerns
	}
	return nil
}

func deriveWorkerBlockReason(block runtime.VerkResultBlock, found bool, resultText string, exitCode int) string {
	if found && strings.TrimSpace(block.BlockReason) != "" {
		return strings.TrimSpace(block.BlockReason)
	}
	// If the CLI failed without a verk result block, include the CLI's output
	// so the user can see why (e.g., "Not logged in", auth errors).
	if exitCode != 0 && resultText != "" {
		reason := strings.TrimSpace(resultText)
		if len(reason) > 120 {
			reason = reason[:117] + "..."
		}
		return reason
	}
	return ""
}

func deriveWorkerRetryClass(block runtime.VerkResultBlock, found, cliOK bool, exitCode int, stderr []byte) runtime.RetryClass {
	if found {
		status, ok := normalizeWorkerStatusString(block.Status)
		if ok {
			return retryClassForStatus(status, exitCode, stderr)
		}
	}
	if cliOK && exitCode == 0 {
		return runtime.RetryClassTerminal
	}
	if looksLikeTransientFailure(stderr) {
		return runtime.RetryClassRetryable
	}
	if looksLikeMissingContext(stderr) {
		return runtime.RetryClassBlockedByOperatorInput
	}
	return runtime.RetryClassRetryable
}

func deriveReviewWorkerStatus(block runtime.VerkReviewBlock, found, cliOK bool, findings []runtime.ReviewFinding, threshold runtime.Severity, exitCode int, stderr []byte) runtime.WorkerStatus {
	if cliOK && exitCode == 0 {
		if deriveReviewStatus(findings, threshold) == runtime.ReviewStatusFindings {
			return runtime.WorkerStatusDoneWithConcerns
		}
		return runtime.WorkerStatusDone
	}
	if looksLikeMissingContext(stderr) {
		return runtime.WorkerStatusNeedsContext
	}
	return runtime.WorkerStatusBlocked
}

func deriveReviewCompletionCode(block runtime.VerkReviewBlock, found bool, exitCode int) string {
	if found && strings.TrimSpace(block.ReviewStatus) != "" {
		return strings.TrimSpace(block.ReviewStatus)
	}
	return fmt.Sprintf("exit_%d", exitCode)
}

func deriveReviewRetryClass(block runtime.VerkReviewBlock, found, cliOK bool, exitCode int, stderr []byte) runtime.RetryClass {
	if cliOK && exitCode == 0 {
		return runtime.RetryClassTerminal
	}
	if looksLikeTransientFailure(stderr) {
		return runtime.RetryClassRetryable
	}
	if looksLikeMissingContext(stderr) {
		return runtime.RetryClassBlockedByOperatorInput
	}
	return runtime.RetryClassRetryable
}

func extractReviewSummary(block runtime.VerkReviewBlock, found bool) string {
	if found {
		return strings.TrimSpace(block.Summary)
	}
	return ""
}

// --- Finding normalization from verk-review blocks ---

func normalizeBlockFindings(block runtime.VerkReviewBlock, found bool) ([]runtime.ReviewFinding, error) {
	if !found {
		return nil, nil
	}
	findings := make([]runtime.ReviewFinding, 0, len(block.Findings))
	for i, raw := range block.Findings {
		finding, err := normalizeFinding(i, raw)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func normalizeFinding(index int, raw runtime.RawFinding) (runtime.ReviewFinding, error) {
	severity, err := normalizeSeverity(raw.Severity)
	if err != nil {
		return runtime.ReviewFinding{}, err
	}
	disposition, err := normalizeDisposition(raw.Disposition)
	if err != nil {
		return runtime.ReviewFinding{}, err
	}
	if strings.TrimSpace(raw.Title) == "" {
		return runtime.ReviewFinding{}, fmt.Errorf("review finding %d missing title", index+1)
	}
	if strings.TrimSpace(raw.Body) == "" {
		return runtime.ReviewFinding{}, fmt.Errorf("review finding %d missing body", index+1)
	}
	if strings.TrimSpace(raw.File) == "" {
		return runtime.ReviewFinding{}, fmt.Errorf("review finding %d missing file", index+1)
	}
	if raw.Line <= 0 {
		return runtime.ReviewFinding{}, fmt.Errorf("review finding %d missing line", index+1)
	}

	id := strings.TrimSpace(raw.ID)
	if id == "" {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%s|%s|%s|%d|%s", index, severity, raw.Title, raw.Body, raw.File, raw.Line, disposition)))
		id = fmt.Sprintf("finding-%d-%s", index+1, hex.EncodeToString(sum[:4]))
	}

	finding := runtime.ReviewFinding{
		ID:           id,
		Severity:     severity,
		Title:        raw.Title,
		Body:         raw.Body,
		File:         raw.File,
		Line:         raw.Line,
		Disposition:  disposition,
		WaivedBy:     strings.TrimSpace(raw.WaivedBy),
		WaiverReason: strings.TrimSpace(raw.WaiverReason),
	}
	if raw.WaivedAt != "" {
		t, err := time.Parse(time.RFC3339, raw.WaivedAt)
		if err == nil {
			finding.WaivedAt = t.UTC()
		}
	}
	if raw.WaiverExpiresAt != nil && *raw.WaiverExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *raw.WaiverExpiresAt)
		if err == nil {
			expiresAt := t.UTC()
			finding.WaiverExpiresAt = &expiresAt
		}
	}
	if finding.Disposition == runtime.ReviewDispositionWaived {
		if finding.WaivedBy == "" {
			return runtime.ReviewFinding{}, fmt.Errorf("waived review finding %q missing waived_by", finding.ID)
		}
		if finding.WaivedAt.IsZero() {
			return runtime.ReviewFinding{}, fmt.Errorf("waived review finding %q missing waived_at", finding.ID)
		}
		if finding.WaiverReason == "" {
			return runtime.ReviewFinding{}, fmt.Errorf("waived review finding %q missing waiver_reason", finding.ID)
		}
	}
	return finding, nil
}

// checkReviewStatusContradiction detects when the reviewer's self-reported
// review_status disagrees with the status derived from their findings.
// This catches cases like a reviewer claiming "passed" while reporting blocking
// findings, or claiming "findings" when no blocking findings exist.
func checkReviewStatusContradiction(block runtime.VerkReviewBlock, blockFound bool, derived runtime.ReviewStatus) error {
	if !blockFound {
		return nil
	}
	raw := normalizeKey(block.ReviewStatus)
	if raw == "" {
		return nil
	}
	var selfReported runtime.ReviewStatus
	switch raw {
	case "passed":
		selfReported = runtime.ReviewStatusPassed
	case "findings":
		selfReported = runtime.ReviewStatusFindings
	default:
		return nil
	}
	if selfReported != derived {
		return fmt.Errorf("reviewer self-reported review_status %q contradicts derived status %q from findings", selfReported, derived)
	}
	return nil
}

func deriveReviewStatus(findings []runtime.ReviewFinding, threshold runtime.Severity) runtime.ReviewStatus {
	return runtime.ReviewResult{Findings: findings}.DerivedReviewStatus(threshold)
}

func normalizeSeverity(raw string) (runtime.Severity, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case string(runtime.SeverityP0):
		return runtime.SeverityP0, nil
	case string(runtime.SeverityP1):
		return runtime.SeverityP1, nil
	case string(runtime.SeverityP2):
		return runtime.SeverityP2, nil
	case string(runtime.SeverityP3):
		return runtime.SeverityP3, nil
	case string(runtime.SeverityP4):
		return runtime.SeverityP4, nil
	default:
		return "", fmt.Errorf("invalid review severity %q", raw)
	}
}

func normalizeDisposition(raw string) (runtime.ReviewDisposition, error) {
	switch normalizeKey(raw) {
	case "open":
		return runtime.ReviewDispositionOpen, nil
	case "resolved":
		return runtime.ReviewDispositionResolved, nil
	case "waived":
		return runtime.ReviewDispositionWaived, nil
	default:
		return "", fmt.Errorf("invalid review disposition %q", raw)
	}
}

// --- Shared helpers ---

func retryClassForStatus(status runtime.WorkerStatus, exitCode int, stderr []byte) runtime.RetryClass {
	switch status {
	case runtime.WorkerStatusDone, runtime.WorkerStatusDoneWithConcerns:
		return runtime.RetryClassTerminal
	case runtime.WorkerStatusNeedsContext, runtime.WorkerStatusBlocked:
		if exitCode != 0 && looksLikeTransientFailure(stderr) {
			return runtime.RetryClassRetryable
		}
		return runtime.RetryClassBlockedByOperatorInput
	default:
		if exitCode == 0 {
			return runtime.RetryClassTerminal
		}
		return runtime.RetryClassRetryable
	}
}

func normalizeWorkerStatusString(raw string) (runtime.WorkerStatus, bool) {
	return runtime.NormalizeWorkerStatusString(raw)
}

func normalizeKey(raw string) string {
	return runtime.NormalizeKey(raw)
}

func looksLikeTransientFailure(stderr []byte) bool {
	blob := strings.ToLower(string(stderr))
	return strings.Contains(blob, "timeout") || strings.Contains(blob, "temporar") || strings.Contains(blob, "retry") || strings.Contains(blob, "transient")
}

func looksLikeMissingContext(stderr []byte) bool {
	blob := strings.ToLower(string(stderr))
	return strings.Contains(blob, "missing context") ||
		strings.Contains(blob, "no context") ||
		strings.Contains(blob, "insufficient context") ||
		strings.Contains(blob, "lease expired") ||
		strings.Contains(blob, "lease not found") ||
		strings.Contains(blob, "operator not found") ||
		strings.Contains(blob, "operator permission")
}

func ensureRuntime(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func defaultRunCommand(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration) (commandResult, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = env

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := commandResult{
		stdout: stdout.Bytes(),
		stderr: stderr.Bytes(),
	}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.exitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

func writeBytesArtifact(prefix string, data []byte) (string, error) {
	file, err := os.CreateTemp("", prefix+"-*.log")
	if err != nil {
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func writeJSONArtifact(prefix string, payload any) (string, error) {
	file, err := os.CreateTemp("", prefix+"-*.json")
	if err != nil {
		return "", err
	}
	if err := encodeJSON(file, payload); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func rewriteJSONArtifact(path string, payload any) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("artifact path is empty")
	}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return encodeJSON(file, payload)
}

func encodeJSON(file *os.File, payload any) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

// runtimeCommandEnv returns nil to inherit the full parent environment.
// Claude Code needs access to auth credentials (keychain, ~/.claude/) which
// require the ambient environment. Config-specified AuthEnvVars are informational
// only — they document which vars the runtime expects but don't restrict the env.
func runtimeCommandEnv(_ runtime.ExecutionConfig) []string {
	return nil
}

func runtimeCommandTimeout(minutes int) time.Duration {
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

// streamEvent represents a parsed line from stream-json output.
type streamEvent struct {
	Type    string          `json:"type"`
	Subtype string          `json:"subtype,omitempty"`
	Message json.RawMessage `json:"message,omitempty"`

	// Fields from the "result" event
	IsError    bool   `json:"is_error,omitempty"`
	Result     string `json:"result,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	NumTurns   int    `json:"num_turns,omitempty"`
}

// toolUseContent is embedded in assistant message content.
type toolUseContent struct {
	Type  string          `json:"type"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// assistantMessage is the structure of an assistant stream event.
type assistantMessage struct {
	Content []toolUseContent `json:"content"`
}

// defaultRunStreamingCommand executes a command and processes stdout as stream-json,
// calling onProgress for each tool-use event. Returns the collected output.
func defaultRunStreamingCommand(ctx context.Context, binary string, args []string, stdin []byte, env []string, timeout time.Duration, onProgress func(string)) (commandResult, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = env

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return commandResult{}, fmt.Errorf("stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return commandResult{}, fmt.Errorf("start command: %w", err)
	}

	// Process stream-json output line by line
	var allOutput bytes.Buffer
	var resultEvent *streamEvent
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		line := scanner.Bytes()
		allOutput.Write(line)
		allOutput.WriteByte('\n')

		var evt streamEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}

		switch evt.Type {
		case "assistant":
			// Parse tool-use events from assistant messages
			if onProgress != nil {
				var msg assistantMessage
				if err := json.Unmarshal(evt.Message, &msg); err == nil {
					for _, content := range msg.Content {
						if content.Type == "tool_use" {
							summary := summarizeToolUse(content.Name, content.Input)
							if summary != "" {
								onProgress(summary)
							}
						}
					}
				}
			}
		case "result":
			resultEvent = &evt
		}
	}

	if scanErr := scanner.Err(); scanErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait() // wait for stderr copy goroutine to finish before reading stderr
		return commandResult{
			stdout: allOutput.Bytes(),
			stderr: stderr.Bytes(),
		}, fmt.Errorf("scan stream output: %w", scanErr)
	}

	waitErr := cmd.Wait()
	result := commandResult{
		stdout: allOutput.Bytes(),
		stderr: stderr.Bytes(),
	}

	// Extract the result text from the result event
	if resultEvent != nil {
		// For stream-json, the result text is in the result event's Result field
		result.stdout = mustMarshalResultAsJSON(resultEvent)
	}

	if waitErr == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		result.exitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, waitErr
}

// mustMarshalResultAsJSON converts a stream result event to the JSON format
// that ExtractCLIResultText expects (same as --output-format json).
func mustMarshalResultAsJSON(evt *streamEvent) []byte {
	cliOutput := runtime.CLIOutputJSON{
		Type:       "result",
		Subtype:    evt.Subtype,
		IsError:    evt.IsError,
		Result:     evt.Result,
		DurationMS: evt.DurationMS,
		NumTurns:   evt.NumTurns,
	}
	data, _ := json.Marshal(cliOutput)
	return data
}

// summarizeToolUse creates a human-readable summary of a tool call.
func summarizeToolUse(name string, input json.RawMessage) string {
	var params map[string]any
	_ = json.Unmarshal(input, &params)

	switch name {
	case "Read":
		if fp, ok := params["file_path"].(string); ok {
			return fmt.Sprintf("reading %s", shortenPath(fp))
		}
	case "Write":
		if fp, ok := params["file_path"].(string); ok {
			return fmt.Sprintf("writing %s", shortenPath(fp))
		}
	case "Edit":
		if fp, ok := params["file_path"].(string); ok {
			return fmt.Sprintf("editing %s", shortenPath(fp))
		}
	case "Bash":
		if cmd, ok := params["command"].(string); ok {
			if len(cmd) > 50 {
				cmd = cmd[:47] + "..."
			}
			return fmt.Sprintf("$ %s", cmd)
		}
	case "Glob":
		if pattern, ok := params["pattern"].(string); ok {
			return fmt.Sprintf("searching %s", pattern)
		}
	case "Grep":
		if pattern, ok := params["pattern"].(string); ok {
			return fmt.Sprintf("grep %s", pattern)
		}
	default:
		return name
	}
	return name
}

// shortenPath strips the repo root prefix to show relative paths.
func shortenPath(path string) string {
	// Find common prefixes and strip them
	if idx := strings.Index(path, "/internal/"); idx >= 0 {
		return path[idx+1:]
	}
	if idx := strings.Index(path, "/cmd/"); idx >= 0 {
		return path[idx+1:]
	}
	if idx := strings.Index(path, "/pkg/"); idx >= 0 {
		return path[idx+1:]
	}
	// Strip home directory prefix
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func safeRawJSON(data []byte) json.RawMessage {
	if json.Valid(data) {
		return json.RawMessage(data)
	}
	escaped, _ := json.Marshal(string(data))
	return json.RawMessage(escaped)
}
