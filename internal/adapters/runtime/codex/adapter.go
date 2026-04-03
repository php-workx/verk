package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"verk/internal/adapters/runtime"
)

const (
	runtimeName   = "codex"
	defaultBinary = "codex"
)

var (
	runCommand = defaultRunCommand
	now        = time.Now
)

var runtimeEnvAllowlist = []string{
	"HOME",
	"LANG",
	"LC_ALL",
	"PATH",
	"TERM",
	"TMPDIR",
}

type Adapter struct {
	Command string
}

type commandResult struct {
	stdout   []byte
	stderr   []byte
	exitCode int
}

type workerResponse struct {
	Status             string `json:"status"`
	CompletionCode     string `json:"completion_code,omitempty"`
	RetryClass         string `json:"retry_class,omitempty"`
	StdoutPath         string `json:"stdout_path,omitempty"`
	StderrPath         string `json:"stderr_path,omitempty"`
	ResultArtifactPath string `json:"result_artifact_path,omitempty"`
	LeaseID            string `json:"lease_id,omitempty"`
}

type rawFinding struct {
	ID              string     `json:"id,omitempty"`
	Severity        string     `json:"severity,omitempty"`
	Title           string     `json:"title,omitempty"`
	Body            string     `json:"body,omitempty"`
	File            string     `json:"file,omitempty"`
	Line            int        `json:"line,omitempty"`
	Disposition     string     `json:"disposition,omitempty"`
	WaivedBy        string     `json:"waived_by,omitempty"`
	WaivedAt        time.Time  `json:"waived_at,omitempty"`
	WaiverReason    string     `json:"waiver_reason,omitempty"`
	WaiverExpiresAt *time.Time `json:"waiver_expires_at,omitempty"`
}

type reviewResponse struct {
	Status             string       `json:"status"`
	CompletionCode     string       `json:"completion_code,omitempty"`
	RetryClass         string       `json:"retry_class,omitempty"`
	StdoutPath         string       `json:"stdout_path,omitempty"`
	StderrPath         string       `json:"stderr_path,omitempty"`
	ResultArtifactPath string       `json:"result_artifact_path,omitempty"`
	LeaseID            string       `json:"lease_id,omitempty"`
	ReviewStatus       string       `json:"review_status,omitempty"`
	Summary            string       `json:"summary,omitempty"`
	Findings           []rawFinding `json:"findings,omitempty"`
}

type workerArtifact struct {
	Runtime              string                `json:"runtime"`
	Request              runtime.WorkerRequest `json:"request"`
	Response             workerResponse        `json:"response"`
	Normalized           runtime.WorkerResult  `json:"normalized"`
	CapturedStdoutPath   string                `json:"captured_stdout_path"`
	CapturedStderrPath   string                `json:"captured_stderr_path"`
	CapturedArtifactPath string                `json:"captured_artifact_path"`
}

type reviewArtifact struct {
	Runtime              string                `json:"runtime"`
	Request              runtime.ReviewRequest `json:"request"`
	Response             reviewResponse        `json:"response"`
	Normalized           runtime.ReviewResult  `json:"normalized"`
	CapturedStdoutPath   string                `json:"captured_stdout_path"`
	CapturedStderrPath   string                `json:"captured_stderr_path"`
	CapturedArtifactPath string                `json:"captured_artifact_path"`
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

	payload, err := json.Marshal(req)
	if err != nil {
		return runtime.WorkerResult{}, fmt.Errorf("marshal worker request: %w", err)
	}

	args := buildWorkerArgs(req)
	execResult, err := runCommand(ctx, a.binary(), args, payload, runtimeCommandEnv(req.ExecutionConfig), runtimeCommandTimeout(req.ExecutionConfig.WorkerTimeoutMinutes))
	finishedAt := now().UTC()
	if err != nil {
		return runtime.WorkerResult{}, fmt.Errorf("run %s worker: %w", runtimeName, err)
	}

	response, err := decodeWorkerResponse(execResult.stdout)
	if err != nil {
		return runtime.WorkerResult{}, err
	}
	if response.LeaseID == "" {
		response.LeaseID = req.LeaseID
	}
	if response.LeaseID != req.LeaseID {
		return runtime.WorkerResult{}, fmt.Errorf("%s worker result lease_id %q does not match request %q", runtimeName, response.LeaseID, req.LeaseID)
	}

	result := runtime.WorkerResult{
		Status:             normalizeWorkerStatus(response.Status, execResult.exitCode, execResult.stderr),
		CompletionCode:     normalizeCompletionCode(response.CompletionCode, response.Status, execResult.exitCode),
		RetryClass:         normalizeRetryClass(response.RetryClass, response.Status, execResult.exitCode, execResult.stderr),
		StdoutPath:         "",
		StderrPath:         "",
		ResultArtifactPath: "",
		LeaseID:            req.LeaseID,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
	}

	result.StdoutPath, err = writeBytesArtifact(runtimeName+"-worker-stdout", execResult.stdout)
	if err != nil {
		return runtime.WorkerResult{}, err
	}
	result.StderrPath, err = writeBytesArtifact(runtimeName+"-worker-stderr", execResult.stderr)
	if err != nil {
		return runtime.WorkerResult{}, err
	}

	artifact := workerArtifact{
		Runtime:              runtimeName,
		Request:              req,
		Response:             response,
		Normalized:           result,
		CapturedStdoutPath:   result.StdoutPath,
		CapturedStderrPath:   result.StderrPath,
		CapturedArtifactPath: response.ResultArtifactPath,
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

	payload, err := json.Marshal(req)
	if err != nil {
		return runtime.ReviewResult{}, fmt.Errorf("marshal review request: %w", err)
	}

	args := buildReviewArgs(req)
	execResult, err := runCommand(ctx, a.binary(), args, payload, runtimeCommandEnv(req.ExecutionConfig), runtimeCommandTimeout(req.ExecutionConfig.ReviewerTimeoutMinutes))
	finishedAt := now().UTC()
	if err != nil {
		return runtime.ReviewResult{}, fmt.Errorf("run %s reviewer: %w", runtimeName, err)
	}

	response, err := decodeReviewResponse(execResult.stdout)
	if err != nil {
		return runtime.ReviewResult{}, err
	}
	if response.LeaseID == "" {
		response.LeaseID = req.LeaseID
	}
	if response.LeaseID != req.LeaseID {
		return runtime.ReviewResult{}, fmt.Errorf("%s review result lease_id %q does not match request %q", runtimeName, response.LeaseID, req.LeaseID)
	}

	findings, err := normalizeFindings(response.Findings)
	if err != nil {
		return runtime.ReviewResult{}, err
	}

	normalized := runtime.ReviewResult{
		Status:             normalizeReviewStatus(response.Status, response.ReviewStatus, findings, req.EffectiveReviewThreshold, execResult.exitCode, execResult.stderr),
		CompletionCode:     normalizeCompletionCode(response.CompletionCode, response.Status, execResult.exitCode),
		RetryClass:         normalizeRetryClass(response.RetryClass, response.Status, execResult.exitCode, execResult.stderr),
		StdoutPath:         "",
		StderrPath:         "",
		ResultArtifactPath: "",
		LeaseID:            req.LeaseID,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		ReviewStatus:       deriveReviewStatus(findings, req.EffectiveReviewThreshold),
		Summary:            strings.TrimSpace(response.Summary),
		Findings:           findings,
	}

	if response.ReviewStatus != "" {
		reported, err := normalizeReviewStatusString(response.ReviewStatus)
		if err != nil {
			return runtime.ReviewResult{}, err
		}
		if reported != normalized.ReviewStatus {
			return runtime.ReviewResult{}, fmt.Errorf("%s review status %q contradicts derived status %q", runtimeName, reported, normalized.ReviewStatus)
		}
	}

	normalized.StdoutPath, err = writeBytesArtifact(runtimeName+"-review-stdout", execResult.stdout)
	if err != nil {
		return runtime.ReviewResult{}, err
	}
	normalized.StderrPath, err = writeBytesArtifact(runtimeName+"-review-stderr", execResult.stderr)
	if err != nil {
		return runtime.ReviewResult{}, err
	}

	artifact := reviewArtifact{
		Runtime:              runtimeName,
		Request:              req,
		Response:             response,
		Normalized:           normalized,
		CapturedStdoutPath:   normalized.StdoutPath,
		CapturedStderrPath:   normalized.StderrPath,
		CapturedArtifactPath: response.ResultArtifactPath,
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

func buildWorkerArgs(req runtime.WorkerRequest) []string {
	args := []string{"worker", "--lease-id", req.LeaseID, "--runtime", runtimeName}
	appendIf := func(flag, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		args = append(args, flag, value)
	}
	appendIf("--run-id", req.RunID)
	appendIf("--ticket-id", req.TicketID)
	appendIf("--wave-id", req.WaveID)
	if req.Attempt > 0 {
		args = append(args, "--attempt", strconv.Itoa(req.Attempt))
	}
	appendIf("--worktree-path", req.WorktreePath)
	appendIf("--input-artifact-path", req.InputArtifactPath)
	return args
}

func buildReviewArgs(req runtime.ReviewRequest) []string {
	args := []string{"review", "--fresh-context", "--lease-id", req.LeaseID, "--runtime", runtimeName, "--effective-review-threshold", string(req.EffectiveReviewThreshold)}
	appendIf := func(flag, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		args = append(args, flag, value)
	}
	appendIf("--run-id", req.RunID)
	appendIf("--ticket-id", req.TicketID)
	appendIf("--wave-id", req.WaveID)
	if req.Attempt > 0 {
		args = append(args, "--attempt", strconv.Itoa(req.Attempt))
	}
	appendIf("--input-artifact-path", req.InputArtifactPath)
	return args
}

func decodeWorkerResponse(stdout []byte) (workerResponse, error) {
	var response workerResponse
	if len(bytes.TrimSpace(stdout)) == 0 {
		return response, fmt.Errorf("%s worker produced empty stdout", runtimeName)
	}
	if err := json.Unmarshal(stdout, &response); err != nil {
		return response, fmt.Errorf("%s worker output is not valid JSON: %w", runtimeName, err)
	}
	return response, nil
}

func decodeReviewResponse(stdout []byte) (reviewResponse, error) {
	var response reviewResponse
	if len(bytes.TrimSpace(stdout)) == 0 {
		return response, fmt.Errorf("%s reviewer produced empty stdout", runtimeName)
	}
	if err := json.Unmarshal(stdout, &response); err != nil {
		return response, fmt.Errorf("%s reviewer output is not valid JSON: %w", runtimeName, err)
	}
	return response, nil
}

func normalizeWorkerStatus(raw string, exitCode int, stderr []byte) runtime.WorkerStatus {
	if status, ok := normalizeWorkerStatusString(raw); ok {
		return status
	}
	if exitCode == 0 {
		return runtime.WorkerStatusDone
	}
	if looksLikeMissingContext(stderr) {
		return runtime.WorkerStatusNeedsContext
	}
	return runtime.WorkerStatusBlocked
}

func normalizeReviewStatus(statusRaw, reviewStatusRaw string, findings []runtime.ReviewFinding, threshold runtime.Severity, exitCode int, stderr []byte) runtime.WorkerStatus {
	if status, ok := normalizeWorkerStatusString(statusRaw); ok {
		return status
	}
	if exitCode == 0 {
		if deriveReviewStatus(findings, threshold) == runtime.ReviewStatusFindings {
			return runtime.WorkerStatusDoneWithConcerns
		}
		if reviewStatus, err := normalizeReviewStatusString(reviewStatusRaw); err == nil && reviewStatus == runtime.ReviewStatusFindings {
			return runtime.WorkerStatusDoneWithConcerns
		}
		return runtime.WorkerStatusDone
	}
	if looksLikeMissingContext(stderr) {
		return runtime.WorkerStatusNeedsContext
	}
	return runtime.WorkerStatusBlocked
}

func normalizeRetryClass(raw string, statusRaw string, exitCode int, stderr []byte) runtime.RetryClass {
	if retryClass, ok := normalizeRetryClassString(raw); ok {
		return retryClass
	}
	if status, ok := normalizeWorkerStatusString(statusRaw); ok {
		return retryClassForStatus(status, exitCode, stderr)
	}
	if exitCode == 0 {
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

func normalizeCompletionCode(raw, statusRaw string, exitCode int) string {
	if strings.TrimSpace(raw) != "" {
		return strings.TrimSpace(raw)
	}
	if status, ok := normalizeWorkerStatusString(statusRaw); ok {
		return string(status)
	}
	return fmt.Sprintf("exit_%d", exitCode)
}

func normalizeWorkerStatusString(raw string) (runtime.WorkerStatus, bool) {
	switch normalizeKey(raw) {
	case "done", "completed", "complete", "success", "passed", "ok":
		return runtime.WorkerStatusDone, true
	case "done_with_concerns", "donewithconcerns", "concerns":
		return runtime.WorkerStatusDoneWithConcerns, true
	case "needs_context", "needscontext", "context_needed", "needs-more-context":
		return runtime.WorkerStatusNeedsContext, true
	case "blocked", "blocked_by_operator_input", "blockedbyoperatorinput":
		return runtime.WorkerStatusBlocked, true
	default:
		return "", false
	}
}

func normalizeRetryClassString(raw string) (runtime.RetryClass, bool) {
	switch normalizeKey(raw) {
	case "retryable", "retry", "transient":
		return runtime.RetryClassRetryable, true
	case "terminal", "final", "done":
		return runtime.RetryClassTerminal, true
	case "blocked_by_operator_input", "blockedbyoperatorinput", "operator_input", "needs_context", "needscontext":
		return runtime.RetryClassBlockedByOperatorInput, true
	default:
		return "", false
	}
}

func normalizeReviewStatusString(raw string) (runtime.ReviewStatus, error) {
	switch normalizeKey(raw) {
	case "passed", "pass", "clean", "ok":
		return runtime.ReviewStatusPassed, nil
	case "findings", "failed", "fail", "issues":
		return runtime.ReviewStatusFindings, nil
	default:
		return "", fmt.Errorf("invalid review status %q", raw)
	}
}

func normalizeFindings(rawFindings []rawFinding) ([]runtime.ReviewFinding, error) {
	findings := make([]runtime.ReviewFinding, 0, len(rawFindings))
	for i, raw := range rawFindings {
		finding, err := normalizeFinding(i, raw)
		if err != nil {
			return nil, err
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func normalizeFinding(index int, raw rawFinding) (runtime.ReviewFinding, error) {
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
		WaivedAt:     raw.WaivedAt,
		WaiverReason: strings.TrimSpace(raw.WaiverReason),
	}
	if raw.WaiverExpiresAt != nil {
		expiresAt := raw.WaiverExpiresAt.UTC()
		finding.WaiverExpiresAt = &expiresAt
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

func normalizeKey(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	replacer := strings.NewReplacer("-", "_", " ", "_")
	return replacer.Replace(raw)
}

func looksLikeTransientFailure(stderr []byte) bool {
	blob := strings.ToLower(string(stderr))
	return strings.Contains(blob, "timeout") || strings.Contains(blob, "temporar") || strings.Contains(blob, "retry") || strings.Contains(blob, "transient")
}

func looksLikeMissingContext(stderr []byte) bool {
	blob := strings.ToLower(string(stderr))
	return strings.Contains(blob, "context") || strings.Contains(blob, "input") || strings.Contains(blob, "operator") || strings.Contains(blob, "lease")
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
		file.Close()
		os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
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
		file.Close()
		os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
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
	defer file.Close()
	return encodeJSON(file, payload)
}

func encodeJSON(file *os.File, payload any) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func runtimeCommandEnv(cfg runtime.ExecutionConfig) []string {
	names := make([]string, 0, len(runtimeEnvAllowlist)+len(cfg.AuthEnvVars))
	names = append(names, runtimeEnvAllowlist...)
	names = append(names, cfg.AuthEnvVars...)

	seen := make(map[string]struct{}, len(names))
	env := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.TrimSpace(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	sort.Strings(env)
	return env
}

func runtimeCommandTimeout(minutes int) time.Duration {
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}
