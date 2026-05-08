package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// gitIsolationKeys lists environment variables that must be stripped from the
// parent process before spawning isolated worker subprocesses. Without this, a
// host running verk inside a git worktree or with GIT_DIR set would leak that
// state into workers, defeating worktree isolation.
// GitIsolationKeys returns the list of environment variable keys that must be
// stripped before spawning isolated worker subprocesses.
func GitIsolationKeys() []string {
	return gitIsolationKeys
}

var gitIsolationKeys = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_COMMON_DIR",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_PREFIX",
	"GIT_SUPER_PREFIX",
}

// StripEnvKeys removes entries whose key matches any of blockedKeys from env.
// The returned slice has no duplicate keys; the first occurrence wins.
func StripEnvKeys(env []string, blockedKeys ...string) []string {
	blocked := make(map[string]struct{}, len(blockedKeys))
	for _, k := range blockedKeys {
		blocked[k] = struct{}{}
	}
	out := make([]string, 0, len(env))
	for _, pair := range env {
		key, _, ok := strings.Cut(pair, "=")
		if ok {
			if _, skip := blocked[key]; skip {
				continue
			}
		}
		out = append(out, pair)
	}
	return out
}

// BuildIsolatedProcessEnv overlays common temp/cache variables onto baseEnv so
// worker, reviewer, and verification subprocesses keep ephemeral outputs under
// Verk-managed state instead of polluting the repo or ticket worktree.
//
// stateHint should be a repo root or worktree path. If <stateHint>/.verk is a
// symlink, the helper resolves it first so caches land in shared engine state.
func BuildIsolatedProcessEnv(baseEnv []string, stateHint string) ([]string, error) {
	if strings.TrimSpace(stateHint) == "" {
		return append([]string(nil), baseEnv...), nil
	}

	stateRoot, err := resolveProcessStateRoot(stateHint)
	if err != nil {
		return nil, err
	}
	cacheRoot := filepath.Join(stateRoot, "process-cache")

	dirs := map[string]string{
		"tmp":            filepath.Join(cacheRoot, "tmp"),
		"go-build":       filepath.Join(cacheRoot, "go-build"),
		"go-tmp":         filepath.Join(cacheRoot, "go-tmp"),
		"xdg":            filepath.Join(cacheRoot, "xdg"),
		"npm":            filepath.Join(cacheRoot, "npm"),
		"pip":            filepath.Join(cacheRoot, "pip"),
		"python-pycache": filepath.Join(cacheRoot, "python-pycache"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create isolated process cache dir %q: %w", dir, err)
		}
	}

	envMap := make(map[string]string, len(baseEnv)+9)
	order := make([]string, 0, len(baseEnv)+9)
	for _, pair := range baseEnv {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		if _, exists := envMap[key]; !exists {
			order = append(order, key)
		}
		envMap[key] = value
	}

	set := func(key, value string) {
		if _, exists := envMap[key]; !exists {
			order = append(order, key)
		}
		envMap[key] = value
	}

	set("TMPDIR", dirs["tmp"])
	set("TMP", dirs["tmp"])
	set("TEMP", dirs["tmp"])
	set("GOCACHE", dirs["go-build"])
	set("GOTMPDIR", dirs["go-tmp"])
	set("XDG_CACHE_HOME", dirs["xdg"])
	set("npm_config_cache", dirs["npm"])
	set("PIP_CACHE_DIR", dirs["pip"])
	set("PYTHONPYCACHEPREFIX", dirs["python-pycache"])

	sort.Strings(order)
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+envMap[key])
	}
	return out, nil
}

func resolveProcessStateRoot(stateHint string) (string, error) {
	absHint, err := filepath.Abs(strings.TrimSpace(stateHint))
	if err != nil {
		return "", fmt.Errorf("resolve process state hint %q: %w", stateHint, err)
	}
	verkPath := filepath.Join(absHint, ".verk")
	if resolved, err := filepath.EvalSymlinks(verkPath); err == nil {
		return filepath.Clean(resolved), nil
	}
	// If the hint points at a linked worktree (.git is a file, not a directory),
	// a missing .verk link means the cache-root is broken — fail fast instead of
	// silently creating a disposable .verk under the worktree.
	if gitInfo, err := os.Lstat(filepath.Join(absHint, ".git")); err == nil && !gitInfo.IsDir() {
		return "", fmt.Errorf("linked worktree %q is missing a usable .verk symlink", absHint)
	}
	if err := os.MkdirAll(verkPath, 0o755); err != nil {
		return "", fmt.Errorf("create process state root %q: %w", verkPath, err)
	}
	return filepath.Clean(verkPath), nil
}
