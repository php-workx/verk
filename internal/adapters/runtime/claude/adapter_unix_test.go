//go:build unix
// +build unix

package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestRunStreamingCommand_NormalCompletion verifies that a subprocess producing
// valid stream-json output is processed correctly and no error is returned.
func TestRunStreamingCommand_NormalCompletion(t *testing.T) {
	resultLine := `{"type":"result","subtype":"success","result":"hello","is_error":false,"duration_ms":100,"num_turns":1}`
	result, err := defaultRunStreamingCommand(
		context.Background(),
		"/bin/sh",
		[]string{"-c", fmt.Sprintf("printf '%%s\n' '%s'", resultLine)},
		nil,
		nil,
		10*time.Second,
		nil,
	)
	if err != nil {
		t.Fatalf("expected no error on normal completion, got: %v", err)
	}
	if len(result.stdout) == 0 {
		t.Fatal("expected non-empty stdout in result")
	}
	// The result stdout should be the marshalled result event JSON.
	var out map[string]any
	if err := json.Unmarshal(result.stdout, &out); err != nil {
		t.Fatalf("result stdout is not valid JSON: %v — raw: %s", err, result.stdout)
	}
	if out["result"] != "hello" {
		t.Fatalf("expected result field 'hello', got %v", out["result"])
	}
}
