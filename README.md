# VERK

## Worktree cache location

Set `VERK_WORKTREE_ROOT` to force where per-ticket worktree caches are created.

If unset, path resolution falls back in order to:

1. `$XDG_CACHE_HOME/verk/worktrees`
2. `$HOME/.cache/verk/worktrees`
3. `os.TempDir()/verk/worktrees`

The cache root is then namespaced by repository hash:
`<workRoot>/<repoHash>`, where `<repoHash> = sha256(abs(repoRoot))[:12]`.
Ticket worktrees are created at:
`<workRoot>/<runID>/<ticketID>/`.
