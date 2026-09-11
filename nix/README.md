# Nix layout

| File | Role |
| --- | --- |
| `package.nix` | the `huginn` derivation (`huginn`, `huginn-channel`) |
| `sidecar-options.nix` | options shared by home-manager and NixOS |
| `lib.nix` | argv + wrapper that cats `tokenFile` at exec |
| `home-module.nix` | sidecar as a user service (systemd user / launchd) |
| `nixos-module.nix` | sidecar with an explicit `user` |

home-manager is the primary way to run this: the sidecar reads `~/.grok`,
`~/.claude`, and `~/.codex`.

`services.huginn`: `enable`, `tokenFile`, `bind` (default loopback),
`extraArgs`.
