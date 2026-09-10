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

Machine topology is a **user sidecar** (home-manager) plus a **system**
zmqcat join. The hub owns `zmqcat serve` and, separately, huginn-mcp; do
not run huginn-mcp on the session machines.

`services.huginn.zmqcatListen` must equal `services.zmqcat.listen`:

- Linux: `unix:///run/zmqcat/bus.sock`
- Darwin: `unix:///var/lib/zmqcat/bus.sock`

The wrapper in `lib.nix` cats `tokenFile` at exec so the secret never
lands in the store. Rotate by replacing the file and restarting the
service; there is no rotation API.
