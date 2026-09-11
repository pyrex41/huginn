# Install

One process on the machine whose sessions you want to share. Network is
yours (loopback, WireGuard, Tailscale). One token is the capability.

## 1. Token

```sh
mkdir -p ~/.config/huginn
openssl rand -hex 32 > ~/.config/huginn/token
chmod 600 ~/.config/huginn/token
```

## 2. Run

```sh
HUGINN_TOKEN="$(cat ~/.config/huginn/token)" huginn serve
# 127.0.0.1:7419
```

Share on an overlay you already have:

```sh
HUGINN_TOKEN="$(cat ~/.config/huginn/token)" huginn serve --bind 10.8.0.2:7419
```

`--bind` must be loopback or a private address (RFC1918, Tailscale
`100.64/10`, IPv6 ULA). `0.0.0.0` is refused. An overlay bind also listens
on `127.0.0.1` of the same port so `huginn list` and the Claude plugin keep
working.

Nix (home-manager, as you):

```nix
imports = [ inputs.huginn.homeManagerModules.default ];
services.huginn = {
  enable = true;
  tokenFile = "${config.home.homeDirectory}/.config/huginn/token";
  # bind = "10.8.0.2:7419";
};
```

NixOS can run the same sidecar as `services.huginn.user` (that human, not
root). macOS uses the home-manager launchd agent.

Without Nix: `make build` or
`go install github.com/pyrex41/huginn/cmd/huginn@latest`.

## 3. Point a harness at it

MCP:

```json
{
  "mcpServers": {
    "huginn": {
      "type": "http",
      "url": "http://10.8.0.2:7419/mcp",
      "headers": { "Authorization": "Bearer TOKEN" }
    }
  }
}
```

ACP (grokbot, Zed, or spawn `huginn acp`): WebSocket
`ws://10.8.0.2:7419/acp` with the same Bearer.

JSON-RPC: `POST http://10.8.0.2:7419/` with the same header.

`POST /mcp` is the harness door. The Claude **channel plugin**
(`huginn-channel`, project `.mcp.json`) is a different binary: it injects
into a live Claude TUI on this machine. Load it with
`claude --dangerously-load-development-channels server:huginn`.

## Verify

```sh
huginn list --liveness live --token "$(cat ~/.config/huginn/token)"
```

Rows with `join=none` are live but not attachable yet (no Grok leader, no
Claude channel plugin, no Codex app-server). That is honest, not a transport
bug.

## Trust

The token is sitting at that keyboard: list, prompt, interrupt, and
permission (Bash/Write) on this host. Do not put the listener on a public
address. Rotate by replacing `tokenFile` and restarting.
