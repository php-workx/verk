# Cross-Platform Review Checklist

Always injected unconditionally by standards.go.

---

## File System

- [ ] **Hardcoded path separators**: Is `/` or `\` used to build paths? Use language
  path APIs: `filepath.Join` (Go), `os.path.join` (Python), `path.join` (Node),
  `Path::new` (Rust). Bad: `dir + "/" + file`. Good: `filepath.Join(dir, file)`.

- [ ] **`os.Rename` atomicity on Windows**: `os.Rename` is not atomic on Windows —
  it can fail if the destination exists and does not provide crash-safe guarantees.
  For atomic writes, use write-temp-then-rename, and handle Windows rename semantics
  explicitly or with a cross-platform library.

- [ ] **`os.WriteFile` in-place**: Overwrites in-place — a crash mid-write leaves a
  truncated or corrupt file. Use write-to-temp + rename for crash safety.

- [ ] **Case-sensitivity assumptions**: macOS (APFS) and Windows (NTFS) are
  case-insensitive; Linux (ext4) is case-sensitive. A file created as `Config.yaml`
  and opened as `config.yaml` works on macOS but fails on Linux.

- [ ] **Symlink portability**: Creating symlinks on Windows requires elevated privileges
  or Developer Mode. Guard symlink operations with platform checks.

- [ ] **Max path length on Windows**: Windows enforces 260-character `MAX_PATH` by
  default. Deeply nested generated paths can silently fail.

---

## File Locking

- [ ] **Flock error discrimination**: All `syscall.Flock` / `fcntl` errors should not
  be reported as the same "already being executed" message. Distinguish:
  - `EAGAIN` / `EWOULDBLOCK` → lock contention (another process holds it)
  - `EBADF`, `EINVAL`, permission errors → distinct failure — report the underlying error

- [ ] **Advisory vs mandatory locking**: Unix `flock` is advisory (processes can ignore
  it); Windows file locks are mandatory. Code relying on advisory locking semantics
  will not enforce on Windows and vice versa.

---

## Environment

- [ ] **Hardcoded `/tmp`**: Use `os.TempDir()` (Go), `tempfile.gettempdir()` (Python),
  `os.tmpdir()` (Node), `std::env::temp_dir()` (Rust). The temp path varies by OS.

- [ ] **`os.Getwd()` as repo root fallback**: `os.Getwd()` returns the process working
  directory, not the git repo root. Running from a subdirectory gives the wrong path.
  Use the git adapter (`git rev-parse --show-toplevel`) instead.

- [ ] **`nil` env replaced with `[]string{}`**: For `exec.Cmd.Env`, `nil` = inherit
  parent environment; `[]string{}` = empty environment. The latter breaks all
  PATH-dependent commands (go, git, npm). Never replace nil with an empty slice.

- [ ] **Environment variable case sensitivity**: Linux env vars are case-sensitive;
  Windows env vars are case-insensitive. `PATH` and `Path` differ on Linux.

---

## Process & Signals

- [ ] **Unix signal handling on Windows**: `SIGTERM`, `SIGHUP`, and other Unix signals
  are not available on Windows. Use cross-platform shutdown libraries or add
  platform-specific signal handling behind build tags.

- [ ] **Shell command assumptions**: `sh -c`, `grep`, `sed`, `find` do not exist on
  Windows. Use language-native alternatives or ensure cross-platform tooling.

---

## Git Path Handling

- [ ] **`physicalRoot` vs `displayRoot`**: The symlink-resolved physical path and the
  git-canonical display path (`git rev-parse --show-toplevel`) differ on systems
  with symlinked worktrees. Path relativization must use the display root so that
  relative paths computed from it match what the user sees.

- [ ] **Run directory ordering by mtime**: Filesystem mtime is unreliable for ordering
  (can be modified by non-application operations). Use filename-embedded timestamps
  or an explicit index file instead.
