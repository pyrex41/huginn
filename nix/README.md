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
