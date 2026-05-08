package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIsolatedProcessEnv_SetsCacheVarsUnderVerkState(t *testing.T) {
	workDir := t.TempDir()

	env, err := BuildIsolatedProcessEnv([]string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
	}, workDir)
	if err != nil {
		t.Fatalf("BuildIsolatedProcessEnv: %v", err)
	}

	envMap := make(map[string]string, len(env))
	for _, pair := range env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		envMap[key] = value
	}

	expectedPrefix := filepath.Join(workDir, ".verk", "process-cache")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP", "GOCACHE", "GOTMPDIR", "XDG_CACHE_HOME", "npm_config_cache", "PIP_CACHE_DIR", "PYTHONPYCACHEPREFIX"} {
		value := envMap[key]
		if strings.TrimSpace(value) == "" {
			t.Fatalf("expected %s to be set", key)
		}
		if !strings.HasPrefix(filepath.Clean(value), filepath.Clean(expectedPrefix)+string(filepath.Separator)) &&
			filepath.Clean(value) != filepath.Clean(expectedPrefix) {
			t.Fatalf("expected %s under %q, got %q", key, expectedPrefix, value)
		}
		if info, err := os.Stat(value); err != nil || !info.IsDir() {
			t.Fatalf("expected %s directory to exist at %q: stat=%v", key, value, err)
		}
	}

	if envMap["PATH"] != "/usr/bin" {
		t.Fatalf("expected PATH preserved, got %q", envMap["PATH"])
	}
	if envMap["HOME"] != "/tmp/home" {
		t.Fatalf("expected HOME preserved, got %q", envMap["HOME"])
	}
}

func TestBuildIsolatedProcessEnv_EmptyWorkDirPreservesBaseEnv(t *testing.T) {
	env, err := BuildIsolatedProcessEnv([]string{"PATH=/usr/bin"}, "")
	if err != nil {
		t.Fatalf("BuildIsolatedProcessEnv: %v", err)
	}
	if len(env) != 1 || env[0] != "PATH=/usr/bin" {
		t.Fatalf("expected unchanged env, got %v", env)
	}
}
