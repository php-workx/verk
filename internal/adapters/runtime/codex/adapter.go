package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"verk/internal/adapters/runtime"
	"verk/internal/adapters/runtime/llmclibridge"
	"verk/internal/state"
)

const (
	runtimeName   = "codex"
	defaultBinary = "codex"
)

var (
	newBridge = func() bridgeClient { return llmclibridge.New() }
	now       = time.Now
)

type bridgeClient interface {
	Run(context.Context, llmclibridge.Request) (llmclibridge.Result, error)
	CheckAvailability(context.Context, string, string) error
}

type Adapter struct {
	Command string
	bridge  bridgeClient
}

type commandResult struct {
	stdout     []byte
	stderr     []byte
	resultText string
	exitCode   int
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

type intentArtifact struct {
	Runtime            string                   `json:"runtime"`
	Request            runtime.IntentRequest    `json:"request"`
	CLIOutput          json.RawMessage          `json:"cli_output"`
	IntentBlock        *runtime.VerkIntentBlock `json:"intent_block,omitempty"`
	Normalized         runtime.IntentResult     `json:"normalized"`
	CapturedStdoutPath string                   `json:"captured_stdout_path"`
	CapturedStderrPath string                   `json:"captured_stderr_path"`
}

func New() *Adapter {
	return &Adapter{Command: defaultBinary, bridge: newBridge()}
}

func NewWithCommand(command string) *Adapter {
	command = strings.TrimSpace(command)
	if command == "" {
		command = defaultBinary
	}
	return &Adapter{Command: command, bridge: newBridge()}
}

func (a *Adapter) CheckAvailability(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.runtimeBridge().CheckAvailability(ctx, runtimeName, a.binary()); err != nil {
		return fmt.Errorf("%s availability check failed: %w", runtimeName, err)
	}
	return nil
}

func (a *Adapter) RunWorker(ctx context.Context, req runtime.WorkerRequest) (runtime.WorkerResult, error) {
	if err := validateWorkerRequest(req); err != nil {
		return runtime.WorkerResult{}, err
	}

	req.Runtime = ensureRuntime(req.Runtime, runtimeName)
	startedAt := now().UTC()

	prompt := runtime.BuildWorkerPrompt(req)
	execResult, err := a.runCodexRequest(ctx, runtime.WorkerSystemPrompt(), prompt, req.Model, req.Reasoning, req.WorktreePath, runtimeCommandTimeout(req.ExecutionConfig.WorkerTimeoutMinutes), req.OnProgress)
	finishedAt := now().UTC()
	if err != nil {
		return runtime.WorkerResult{}, fmt.Errorf("run %s worker: %w", runtimeName, err)
	}

	resultText := execResult.parseText()
	resultBlock, blockFound := runtime.ParseResultBlock(resultText)

	result := runtime.WorkerResult{
		Status:         deriveWorkerStatus(resultBlock, blockFound, execResult.exitCode, execResult.stdout, execResult.stderr),
		CompletionCode: deriveWorkerCompletionCode(resultBlock, blockFound, execResult.exitCode),
		Concerns:       deriveWorkerConcerns(resultBlock, blockFound),
		BlockReason:    deriveWorkerBlockReason(resultBlock, blockFound, resultText, execResult.exitCode, execResult.stdout, execResult.stderr),
		RetryClass:     deriveWorkerRetryClass(resultBlock, blockFound, execResult.exitCode, execResult.stdout, execResult.stderr),
		LeaseID:        req.LeaseID,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
	}
	result.TokenUsage, result.ActivityStats = extractCodexTelemetry(execResult.stdout)

	result.StdoutPath, err = writeBytesArtifact(runtimeName+"-worker-stdout", execResult.artifactStdout())
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
		CLIOutput:          safeRawJSON(execResult.artifactStdout()),
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

	prompt := runtime.BuildReviewPrompt(req)
	execResult, err := a.runCodexRequest(ctx, runtime.ReviewerSystemPrompt(), prompt, req.Model, req.Reasoning, req.WorktreePath, runtimeCommandTimeout(req.ExecutionConfig.ReviewerTimeoutMinutes), req.OnProgress)
	finishedAt := now().UTC()
	if err != nil {
		return runtime.ReviewResult{}, fmt.Errorf("run %s reviewer: %w", runtimeName, err)
	}

	resultText := execResult.parseText()
	reviewBlock, blockFound := runtime.ParseReviewBlock(resultText)

	findings, err := normalizeBlockFindings(reviewBlock, blockFound)
	if err != nil {
		return runtime.ReviewResult{}, err
	}

	normalized := runtime.ReviewResult{
		Status:         deriveReviewWorkerStatus(reviewBlock, blockFound, findings, req.EffectiveReviewThreshold, execResult.exitCode, execResult.stdout, execResult.stderr),
		CompletionCode: deriveReviewCompletionCode(reviewBlock, blockFound, execResult.exitCode),
		RetryClass:     deriveReviewRetryClass(reviewBlock, blockFound, execResult.exitCode, execResult.stdout, execResult.stderr),
		LeaseID:        req.LeaseID,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		ReviewStatus:   deriveReviewStatus(findings, req.EffectiveReviewThreshold),
		Summary:        extractReviewSummary(reviewBlock, blockFound),
		Findings:       findings,
	}
	normalized.TokenUsage, normalized.ActivityStats = extractCodexTelemetry(execResult.stdout)

	if err := checkReviewStatusContradiction(reviewBlock, blockFound, normalized.ReviewStatus); err != nil {
		return runtime.ReviewResult{}, err
	}

	normalized.StdoutPath, err = writeBytesArtifact(runtimeName+"-review-stdout", execResult.artifactStdout())
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
		CLIOutput:          safeRawJSON(execResult.artifactStdout()),
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

func (a *Adapter) RunIntent(ctx context.Context, req runtime.IntentRequest) (runtime.IntentResult, error) {
	if err := validateIntentRequest(req); err != nil {
		return runtime.IntentResult{}, err
	}

	req.Runtime = ensureRuntime(req.Runtime, runtimeName)
	prompt := runtime.BuildIntentPrompt(req)
	execResult, err := a.runCodexRequest(ctx, runtime.IntentSystemPrompt(), prompt, req.Model, req.Reasoning, req.WorktreePath, 0, nil)
	if err != nil {
		return runtime.IntentResult{}, fmt.Errorf("run %s intent: %w", runtimeName, err)
	}

	resultText := execResult.parseText()
	intentBlock, blockFound := runtime.ParseIntentBlock(resultText)
	if !blockFound {
		return runtime.IntentResult{}, fmt.Errorf("run %s intent: missing intent result block", runtimeName)
	}

	result := runtime.IntentResult{
		CoveredCriteria: intentBlock.CoveredCriteria,
		TargetFiles:     intentBlock.TargetFiles,
		TestPlan:        intentBlock.TestPlan,
		RawResponse:     resultText,
	}

	stdoutPath, err := writeBytesArtifact(runtimeName+"-intent-stdout", execResult.artifactStdout())
	if err != nil {
		return runtime.IntentResult{}, err
	}
	stderrPath, err := writeBytesArtifact(runtimeName+"-intent-stderr", execResult.stderr)
	if err != nil {
		return runtime.IntentResult{}, err
	}
	blockPtr := &intentBlock
	artifact := intentArtifact{
		Runtime:            runtimeName,
		Request:            req,
		CLIOutput:          safeRawJSON(execResult.artifactStdout()),
		IntentBlock:        blockPtr,
		Normalized:         result,
		CapturedStdoutPath: stdoutPath,
		CapturedStderrPath: stderrPath,
	}
	if _, err := writeJSONArtifact(runtimeName+"-intent-result", artifact); err != nil {
		return runtime.IntentResult{}, err
	}

	return result, nil
}

func extractCodexTelemetry(stdout []byte) (*state.RuntimeTokenUsage, *state.RuntimeActivityStats) {
	stats := &state.RuntimeActivityStats{}
	usage := &state.RuntimeTokenUsage{}
	forEachCodexJSONLLine(stdout, func(line string) bool {
		if line == "" {
			return true
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
			Usage struct {
				InputTokens       int64 `json:"input_tokens"`
				CachedInputTokens int64 `json:"cached_input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
				TotalTokens       int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil || event.Type == "" {
			return true
		}
		stats.EventCount++
		if event.Type == "item.completed" {
			switch event.Item.Type {
			case "command_execution":
				stats.CommandCount++
			case "agent_message":
				stats.AgentMessageCount++
			}
		}
		if event.Type == "turn.completed" {
			usage.InputTokens += event.Usage.InputTokens
			usage.CachedInputTokens += event.Usage.CachedInputTokens
			usage.OutputTokens += event.Usage.OutputTokens
			usage.TotalTokens += event.Usage.TotalTokens
		}
		return true
	})
	if usage.TotalTokens == 0 && (usage.InputTokens != 0 || usage.OutputTokens != 0) {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if usage.InputTokens == 0 && usage.CachedInputTokens == 0 && usage.OutputTokens == 0 && usage.TotalTokens == 0 {
		usage = nil
	}
	if stats.EventCount == 0 {
		stats = nil
	}
	return usage, stats
}

func forEachCodexJSONLLine(stdout []byte, visit func(line string) bool) {
	reader := bufio.NewReader(bytes.NewReader(stdout))
	for {
		line, err := reader.ReadString('\n')
		if line != "" && !visit(strings.TrimSpace(line)) {
			return
		}
		if err != nil {
			return
		}
	}
}

func (a *Adapter) binary() string {
	if strings.TrimSpace(a.Command) == "" {
		return defaultBinary
	}
	return a.Command
}

func (a *Adapter) runtimeBridge() bridgeClient {
	if a.bridge != nil {
		return a.bridge
	}
	return newBridge()
}

func (a *Adapter) runCodexRequest(ctx context.Context, systemPrompt, userPrompt, model, reasoning, worktreePath string, timeout time.Duration, onProgress func(string)) (commandResult, error) {
	result, err := a.runtimeBridge().Run(ctx, llmclibridge.Request{
		RuntimeName:  runtimeName,
		Command:      a.binary(),
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
		Model:        model,
		Reasoning:    reasoning,
		WorktreePath: worktreePath,
		Timeout:      timeout,
		OnProgress:   onProgress,
	})
	if err != nil {
		return commandResult{}, err
	}
	return commandResultFromBridge(result), nil
}

func commandResultFromBridge(result llmclibridge.Result) commandResult {
	return commandResult{
		stdout:     result.Stdout,
		stderr:     result.Stderr,
		resultText: result.Text,
		exitCode:   result.ExitCode,
	}
}

func (r commandResult) parseText() string {
	if strings.TrimSpace(r.resultText) != "" {
		return r.resultText
	}
	resultText, _ := runtime.ExtractCLIResultText(r.stdout)
	return resultText
}

func (r commandResult) artifactStdout() []byte {
	if len(r.stdout) > 0 {
		return r.stdout
	}
	return []byte(r.resultText)
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

func validateIntentRequest(req runtime.IntentRequest) error {
	if strings.TrimSpace(req.LeaseID) == "" {
		return fmt.Errorf("intent request missing lease_id")
	}
	return nil
}

// --- Status derivation from verk protocol blocks ---

func deriveWorkerStatus(block runtime.VerkResultBlock, found bool, exitCode int, stdout, stderr []byte) runtime.WorkerStatus {
	if found {
		if status, ok := normalizeWorkerStatusString(block.Status); ok {
			return status
		}
	}
	if exitCode == 0 {
		return runtime.WorkerStatusDone
	}
	if looksLikeQuotaOrAvailabilityFailure(stdout, stderr) {
		return runtime.WorkerStatusBlocked
	}
	if looksLikeMissingContext(stdout, stderr) {
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

func deriveWorkerBlockReason(block runtime.VerkResultBlock, found bool, resultText string, exitCode int, stdout, stderr []byte) string {
	if found && strings.TrimSpace(block.BlockReason) != "" {
		return strings.TrimSpace(block.BlockReason)
	}
	if message := extractCodexFailureMessage(stdout, stderr); exitCode != 0 && message != "" {
		return message
	}
	if exitCode != 0 && resultText != "" {
		reason := strings.TrimSpace(resultText)
		if len(reason) > 120 {
			reason = reason[:117] + "..."
		}
		return reason
	}
	return ""
}

func deriveWorkerRetryClass(block runtime.VerkResultBlock, found bool, exitCode int, stdout, stderr []byte) runtime.RetryClass {
	if found {
		status, ok := normalizeWorkerStatusString(block.Status)
		if ok {
			return retryClassForStatus(status, exitCode, stdout, stderr)
		}
	}
	if exitCode == 0 {
		return runtime.RetryClassTerminal
	}
	if looksLikeQuotaOrAvailabilityFailure(stdout, stderr) {
		return runtime.RetryClassRetryable
	}
	if looksLikeTransientFailure(stderr) {
		return runtime.RetryClassRetryable
	}
	if looksLikeMissingContext(stdout, stderr) {
		return runtime.RetryClassBlockedByOperatorInput
	}
	return runtime.RetryClassRetryable
}

func deriveReviewWorkerStatus(block runtime.VerkReviewBlock, found bool, findings []runtime.ReviewFinding, threshold runtime.Severity, exitCode int, stdout, stderr []byte) runtime.WorkerStatus {
	if exitCode == 0 {
		if deriveReviewStatus(findings, threshold) == runtime.ReviewStatusFindings {
			return runtime.WorkerStatusDoneWithConcerns
		}
		return runtime.WorkerStatusDone
	}
	if looksLikeQuotaOrAvailabilityFailure(stdout, stderr) {
		return runtime.WorkerStatusBlocked
	}
	if looksLikeMissingContext(stdout, stderr) {
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

func deriveReviewRetryClass(block runtime.VerkReviewBlock, found bool, exitCode int, stdout, stderr []byte) runtime.RetryClass {
	if exitCode == 0 {
		return runtime.RetryClassTerminal
	}
	if looksLikeQuotaOrAvailabilityFailure(stdout, stderr) {
		return runtime.RetryClassRetryable
	}
	if looksLikeTransientFailure(stderr) {
		return runtime.RetryClassRetryable
	}
	if looksLikeMissingContext(stdout, stderr) {
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

func retryClassForStatus(status runtime.WorkerStatus, exitCode int, stdout, stderr []byte) runtime.RetryClass {
	switch status {
	case runtime.WorkerStatusDone, runtime.WorkerStatusDoneWithConcerns:
		return runtime.RetryClassTerminal
	case runtime.WorkerStatusNeedsContext, runtime.WorkerStatusBlocked:
		if looksLikeQuotaOrAvailabilityFailure(stdout, stderr) {
			return runtime.RetryClassRetryable
		}
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

func looksLikeMissingContext(stdout, stderr []byte) bool {
	blob := strings.ToLower(string(stderr) + "\n" + extractCodexFailureMessage(stdout, stderr))
	return strings.Contains(blob, "missing context") ||
		strings.Contains(blob, "need more context") ||
		strings.Contains(blob, "needs context") ||
		strings.Contains(blob, "missing spec") ||
		strings.Contains(blob, "operator input")
}

func looksLikeQuotaOrAvailabilityFailure(stdout, stderr []byte) bool {
	blob := strings.ToLower(strings.Join([]string{
		string(stderr),
		extractCodexFailureMessage(stdout, stderr),
		string(stdout),
	}, "\n"))
	return strings.Contains(blob, "usage limit") ||
		strings.Contains(blob, "rate limit") ||
		strings.Contains(blob, "too many requests") ||
		strings.Contains(blob, "quota") ||
		strings.Contains(blob, "model unavailable") ||
		strings.Contains(blob, "switch to another model") ||
		strings.Contains(blob, "try again at") ||
		strings.Contains(blob, "temporarily unavailable") ||
		strings.Contains(blob, "capacity")
}

func extractCodexFailureMessage(stdout, stderr []byte) string {
	var message string
	forEachCodexJSONLLine(stdout, func(line string) bool {
		if line == "" {
			return true
		}
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return true
		}
		switch event.Type {
		case "error":
			if msg := strings.TrimSpace(event.Message); msg != "" {
				message = msg
				return false
			}
		case "turn.failed":
			if msg := strings.TrimSpace(event.Error.Message); msg != "" {
				message = msg
				return false
			}
		}
		return true
	})
	if message != "" {
		return message
	}
	if msg := strings.TrimSpace(string(stderr)); msg != "" {
		return msg
	}
	return ""
}

func ensureRuntime(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
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

func runtimeCommandTimeout(minutes int) time.Duration {
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}

func safeRawJSON(data []byte) json.RawMessage {
	if json.Valid(data) {
		return json.RawMessage(data)
	}
	escaped, _ := json.Marshal(string(data))
	return json.RawMessage(escaped)
}
