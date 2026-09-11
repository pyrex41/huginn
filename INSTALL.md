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


## 4. Cross-host (grokbot → laptop)

Huginn does **not** ship a bus. Multi-machine is one sidecar URL per host.
Bring your own private overlay. **Tailscale is the path that actually worked**
for grokbot ↔ macOS when both ends are NAT'd (Tailcat alone was not enough
from a cloud VM client; see [#3](https://github.com/pyrex41/huginn/issues/3)).

Recommended shape on the laptop:

1. Run the sidecar on **loopback** (launchd / home-manager KeepAlive).
2. Publish it on the tailnet with Tailscale Serve (macOS Network Extension
   does not reliably deliver to a process bound only on `100.x`).
3. From grokbot (or any peer), call MagicDNS — not the raw `100.x` IP
   (Serve has returned `404` on the IP while the hostname worked).

```sh
# laptop
HUGINN_TOKEN="$(cat ~/.config/huginn/token)" huginn serve --bind 127.0.0.1:7419
tailscale serve --bg --http=7419 http://127.0.0.1:7419
tailscale serve status
# → http://<hostname>.tailXXXX.ts.net:7419/
```

```sh
# grokbot / peer on the same tailnet
curl -sS -H "Authorization: Bearer $(cat mac-token)" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"session/list","params":{"liveness":"live","limit":20}}' \
  http://reubens-macbook-pro.tailXXXX.ts.net:7419/
```

MCP / ACP use the same host with `/mcp` or `/acp`.

**WireGuard** works if one side has a real public UDP endpoint (or a VPS
relay). Two pure-NAT peers without a third host is not a stock WireGuard
topology — use Tailscale (or Headscale) instead.

**`--bind <tailscale-ip>:7419`** remains valid on Linux kernel TUN. Prefer
Serve on macOS.

## Trust

The token is sitting at that keyboard: list, prompt, interrupt, and
permission (Bash/Write) on this host. Do not put the listener on a public
address. Rotate by replacing `tokenFile` and restarting.
