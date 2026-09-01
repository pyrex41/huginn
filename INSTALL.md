# Install

Two repos, one flake input. Add `huginn` and you get both halves: it pulls
`zmqcat` in and re-exports its package and modules.

```nix
# flake.nix
{
  inputs.huginn.url = "github:pyrex41/huginn";
}
```

There are two sides, and they are not symmetric:

| | runs | what it does |
| --- | --- | --- |
| **machine** | as *you* | attaches to your Claude / Codex / Grok sessions and serves them on the bus |
| **hub** | anywhere with a stable address | owns the bus, and answers harnesses over MCP |

Set up the hub once. Add each machine after.

---

## 1. The hub

One box the machines can reach — a VPS, a NAS, the always-on desktop.

```nix
# NixOS
{ inputs, ... }: {
  imports = [ inputs.huginn.nixosModules.default ];

  services.zmqcat = {
    enable = true;
    role = "serve";
    mailbox = "/var/lib/zmqcat/mailbox.json";   # jobs survive a restart
  };

  services.huginn-mcp = {
    enable = true;
    tokenFile = "/run/secrets/huginn-mcp-token";
  };
}
```

Generate the token first — any long random string:

```sh
openssl rand -hex 32 | sudo tee /run/secrets/huginn-mcp-token
sudo chmod 600 /run/secrets/huginn-mcp-token
```

On first start the bus prints a `tc…` token to its log. **That token is how
machines join, and anyone holding it can reach the bus**, so move it like a
password:

```sh
journalctl -u zmqcat | grep '^tc'
```

Restrict who may dial in with `services.zmqcat.allow = [ "nodekey:…" ]`.
Without it, the token alone is enough.

---

## 2. Each machine

The sidecar reads *your* `~/.grok`, `~/.claude`, and `~/.codex`, so it runs
as you, not as root. home-manager is the way in:

```nix
# home.nix
{ inputs, ... }: {
  imports = [ inputs.huginn.homeManagerModules.default ];

  services.huginn = {
    enable = true;
    service = "h.studio";                        # this machine's name on the bus
    tokenFile = "${config.home.homeDirectory}/.config/huginn/token";
  };
}
```

```sh
mkdir -p ~/.config/huginn
openssl rand -hex 32 > ~/.config/huginn/token
chmod 600 ~/.config/huginn/token
```

`service` must be unique across the bus — it *is* the machine's address.

The machine also needs a local socket onto the hub's bus. Put the `tc…`
token from step 1 in a file and join:

```nix
# NixOS, or nix-darwin with darwinModules.default
services.zmqcat = {
  enable = true;
  role = "join";
  tokenFile = "/run/secrets/zmqcat-join-token";
};
```

Same machine as the hub? Skip the join and set
`services.zmqcat.role = "serve"` with `local = true`.

---

## 3. Point your harnesses at it

One URL, every harness, every machine:

```json
{
  "mcpServers": {
    "huginn": {
      "type": "http",
      "url": "http://hub.internal:7420/mcp",
      "headers": { "Authorization": "Bearer YOUR_MCP_TOKEN" }
    }
  }
}
```

Claude Code: `~/.claude.json` or a project `.mcp.json`. Codex and Grok Build
take the same shape in their own config.

Then ask an agent *"what coding sessions are running on my machines?"* — it
calls `machines_list`, then `sessions_list`, and gets every machine at once.

---

## Verify

```sh
# hub: are machines announcing themselves?
zmqcat sub --listen unix:///tmp/zmqcat.sock huginn.presence.

# hub: does the MCP endpoint answer?
curl -s -X POST http://127.0.0.1:7420/mcp \
  -H "Authorization: Bearer $(cat /run/secrets/huginn-mcp-token)" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call",
       "params":{"name":"machines_list","arguments":{}}}'

# machine: is the sidecar up?
huginn list --liveness live --token "$(cat ~/.config/huginn/token)"
```

A machine that never appears in `machines_list` either cannot reach the bus
or is announcing under a name you did not expect. Check its log —
`journalctl --user -u huginn`, or `~/Library/Logs/huginn.log` on macOS.

---

## Without Nix

```sh
go build -o bin/huginn ./cmd/huginn
go build -o bin/huginn-mcp ./cmd/huginn-mcp
```

The modules pass ordinary flags; `huginn serve --help` and
`huginn-mcp --help` show the same options under different names.

---

## What this does to your trust boundary

Read this before enabling it anywhere shared.

- **Tokens are paths, never literals.** Every `tokenFile` option takes a
  path read at runtime. A string in your Nix config lands in the
  world-readable store.
- **The bus has no mailbox ACLs.** Anything that can open the zmqcat socket
  can read and write every mailbox, and the sidecar attaches `HUGINN_TOKEN`
  to requests itself — so socket access is equivalent to authenticated
  huginn RPC. Do not enable this on a host where untrusted local users can
  reach that socket.
- **The MCP endpoint is read-only on purpose.** `prompt`, `interrupt`, and
  `permission` are not exposed. Anything reaching it could otherwise drive
  every session on every machine, and `session/permission` approves `Bash`
  and `Write` in someone else's live session. Those verbs wait on
  per-principal authorization.
- **`--bind` defaults to loopback.** Put an overlay or a TLS terminator in
  front before exposing the MCP endpoint further.
