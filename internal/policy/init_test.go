package policy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDetectProjectTooling_JustfilePrefersAutofixQualityTargets(t *testing.T) {
	dir := t.TempDir()
	justfile := `
format:
	echo format

format-check:
	echo format-check

lint:
	echo lint

lint-check:
	echo lint-check

build-check:
	echo build-check

check:
	echo check
`
	if err := os.WriteFile(filepath.Join(dir, "Justfile"), []byte(justfile), 0o644); err != nil {
		t.Fatalf("write Justfile: %v", err)
	}

	tooling := DetectProjectTooling(dir)
	if len(tooling) != 1 {
		t.Fatalf("expected one tooling entry, got %#v", tooling)
	}

	want := []string{"just format", "just lint", "just build-check"}
	if !reflect.DeepEqual(tooling[0].SuggestedCommands, want) {
		t.Fatalf("suggested commands = %#v, want %#v", tooling[0].SuggestedCommands, want)
	}
}
