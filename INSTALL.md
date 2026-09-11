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
Bring your own private overlay.

**Proven path:** put both machines on the same **Tailscale** tailnet, bind
huginn to the laptop Tailscale IP, supervise the process. No Tailscale
Serve required. See [#3](https://github.com/pyrex41/huginn/issues/3).

### Default: `--bind $(tailscale ip -4):7419`

```sh
# laptop — Tailscale logged in on both hosts first
HUGINN_TOKEN="$(cat ~/.config/huginn/token)" \
  huginn serve --bind "$(tailscale ip -4):7419"
# also listens on 127.0.0.1:7419 for local list / Claude plugin
```

```sh
# grokbot (or any peer on the tailnet)
curl -sS -H "Authorization: Bearer $(cat mac-token)" \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"session/list","params":{"liveness":"live","limit":20}}' \
  http://"$(tailscale ip -4 reubens-macbook-pro)":7419/
# or MagicDNS: http://reubens-macbook-pro.tailXXXX.ts.net:7419/
```

MCP / ACP: same host with `/mcp` or `/acp`.

### Supervise it (important)

A bare `nohup huginn serve … &` started from an agent/CI shell often dies
when that shell exits. That shows up as `connection refused` on the
Tailscale IP and looks like a TUN bug. It is not — the process is gone.

Use launchd (macOS) or the home-manager module with `KeepAlive`:

```xml
<!-- ~/Library/LaunchAgents/local.huginn.serve.plist -->
<!-- ProgramArguments → a wrapper that cats the token and runs:
     huginn serve --bind "$(tailscale ip -4):7419"
     RunAtLoad + KeepAlive true -->
```

```sh
# wrapper sketch (~/.config/huginn/run-serve.sh)
#!/bin/bash
IP="$(/Applications/Tailscale.app/Contents/MacOS/Tailscale ip -4 2>/dev/null || tailscale ip -4)"
exec env HUGINN_TOKEN="$(cat ~/.config/huginn/token)" \
  huginn serve --bind "${IP:-127.0.0.1}:7419"
```

### What did *not* work (so we do not recommend it)

| Approach | Result |
| --- | --- |
| Tailcat as cloud-VM **client** → laptop **server** | meow/DERP handshake timed out (laptop→VM worked) |
| Stock WireGuard, two NAT peers, no VPS | no public UDP endpoint either side |
| `nohup` huginn without KeepAlive | process reaped → refused on `:7419` |
| Tailscale Serve in front of loopback | works, but unnecessary once bind is supervised; not preferred |

**WireGuard** is fine if one side (or a small VPS) has a real public UDP
port. Otherwise Tailscale / Headscale.

Tailscale Serve (`tailscale serve --http=7419 …`) is a **last resort**
only if something still blocks direct TCP after the sidecar is confirmed
listening under KeepAlive.

## Trust

The token is sitting at that keyboard: list, prompt, interrupt, and
permission (Bash/Write) on this host. Do not put the listener on a public
address. Rotate by replacing `tokenFile` and restarting.
