# Nix layout

| File | Role |
| --- | --- |
| `package.nix` | the `huginn` derivation (huginn, huginn-mcp, huginn-channel) |
| `sidecar-options.nix` | machine-side options, shared by all three backends |
| `mcp-options.nix` | orchestration-side options |
| `lib.nix` | turns options into argv, and wraps the binary so tokens are read from a file at runtime rather than baked into the store |
| `home-module.nix` | the sidecar as a user service (systemd user unit / launchd agent) |
| `nixos-module.nix` | the sidecar with an explicit `user`, plus the MCP daemon |
| `darwin-module.nix` | the MCP daemon |

The option sets live in their own files because three backends expose the
same options; duplicating them is how they drift.

`services.huginn.shell` enables the six tmux verbs; `tmuxSocket` selects a
nondefault socket. `services.huginn-mcp.shellWrite` separately enables the
write tools on the hub. Both switches default to false.

The package makes tmux and OpenSSH available to `huginn`. The human
`huginn connect` command additionally needs Shen/Go (`HUGINN_SHEN` or
`--shen`). Tailcat is built in: `huginn share` needs no SSH server or
separate Tailcat CLI. Shen is not required for sharing, the sidecar, or
MCP service. Peers and credentials belong in
the user's config, not the Nix store.
