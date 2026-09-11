# List-fix re-spike 2026-09-11 Mac.lan

Binary: repo `bin/huginn` at `94cc1f8`. Sidecar 127.0.0.1:7419.

`huginn list --liveness live --limit 50`: **2.0s** (was 2–3 min). Total 5: 4 grok-acp-none, 1 claude-channel-unattached. Codex live total **0**.

Grok this TUI `01a087ef-…` still join=none. Claude `32b40a1a-…` still unattached. No zmqcat socket, no `codex app-server`, no second SSH host.
