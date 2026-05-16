package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"verk/internal/policy"
)

func TestCheckRuntimesUsesUserFacingRuntimeNames(t *testing.T) {
	orig := diagnoseRuntime
	t.Cleanup(func() { diagnoseRuntime = orig })

	diagnoseRuntime = func(_ context.Context, runtimeName string) RuntimeCheck {
		if runtimeName == "codex" {
			return RuntimeCheck{
				Runtime:   "codex",
				Available: false,
				Details:   "codex runtime does not support required options: codexJSONL",
			}
		}
		return RuntimeCheck{Runtime: runtimeName, Available: true, Details: "ready"}
	}

	cfg := policy.DefaultConfig()
	cfg.Runtime.DefaultRuntime = "codex"
	cfg.Runtime.Worker = policy.RoleProfile{Runtime: "claude"}

	checks := checkRuntimes(context.Background(), cfg)
	gotNames := make([]string, 0, len(checks))
	for _, check := range checks {
		gotNames = append(gotNames, check.Runtime)
		if strings.Contains(check.Runtime, "codex-exec") || strings.Contains(check.Details, "codex-exec") {
			t.Fatalf("runtime check leaked backend name: %#v", check)
		}
	}
	if want := []string{"claude", "codex"}; !reflect.DeepEqual(gotNames, want) {
		t.Fatalf("runtime names = %#v, want %#v", gotNames, want)
	}
	if checks[1].Available {
		t.Fatalf("expected codex diagnostic to be unavailable")
	}
	if !strings.Contains(checks[1].Details, "codex runtime does not support required options: codexJSONL") {
		t.Fatalf("expected codex diagnostic detail, got %q", checks[1].Details)
	}
}
