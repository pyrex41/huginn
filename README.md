# huginn

A host sidecar so grokbot (or any ACP/MCP client) can attach to live Claude
Code, Codex, and Grok Build sessions as a structured client.

Huginn does not own a PTY, does not paint a terminal, and does not type
keystrokes into a TUI. It discovers agent sessions on a machine, attaches
through each runtime's native control plane, and exposes five verbs: list,
watch, prompt, interrupt, permission verdict.

That is the whole product.

## Install

```sh
HUGINN_TOKEN=$(openssl rand -hex 32)
huginn serve --token "$HUGINN_TOKEN"
# 127.0.0.1:7419
```

Share with a colleague on your WireGuard or Tailscale net:

```sh
huginn serve --token "$HUGINN_TOKEN" --bind 10.8.0.2:7419
```

Same process, same token, three doors:

| Path | Who |
| --- | --- |
| `POST /` | JSON-RPC (the five verbs; `huginn list` / `huginn rpc`) |
| `POST /mcp` | MCP harnesses |
| `/acp` (WebSocket) or `huginn acp` (stdio) | any ACP client (grokbot, Zed, …) |

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

ACP: `ws://10.8.0.2:7419/acp` with the same Bearer, or spawn `huginn acp`.

Nix:

```nix
# flake.nix
inputs.huginn.url = "github:pyrex41/huginn";

# home-manager, as the user whose sessions these are
imports = [ inputs.huginn.homeManagerModules.default ];
services.huginn = {
  enable = true;
  tokenFile = "${config.home.homeDirectory}/.config/huginn/token";
  # bind = "10.8.0.2:7419";  # omit for loopback
};
```

Without Nix: `go install github.com/pyrex41/huginn/cmd/huginn@latest` (and
`.../cmd/huginn-channel` if you want live Claude inject) or `make build`.
**[INSTALL.md](INSTALL.md)** is the walkthrough.

The token is the capability. Whoever can reach the bind and present it can
prompt and approve tools on this host. Rotate by replacing the file and
restarting; there is no rotation API.

## Why this exists

Claude Code, Codex, and Grok Build already speak machine protocols:

| Runtime | Native control plane | How a second client joins a live session |
| --- | --- | --- |
| **Grok Build** | ACP (`grok agent stdio` / `serve` / leader) | Speak ACP. Sessions on disk under `~/.grok/sessions`. |
| **Codex** | App-server JSON-RPC (`codex app-server`, open source) | Run app-server as the long-lived process. TUI is `codex --remote`. grokbot is another client on the same socket. |
| **Claude Code** | Channels (MCP `claude/channel`) plus local UDS peers | A channel plugin injects into the *already open* TUI. Permission prompts can relay. Remote Control is Anthropic's own phone/web client, not a bot SDK. |

Wrapping those TUIs in a multiplexer treats an agent conversation as a
screenful of cells and fights exclusive keyboard leases. Huginn talks to the
agent, not the terminal emulator.

shenmux remains the right tool for an arbitrary shell. It is the wrong layer
for this.

## What a session is here

A session is a **coding-agent conversation** with an ID the runtime already
persists: Claude transcript / session id, Codex `thread.id`, Grok session
UUID. It outlives a particular TUI window when the runtime says it does.

It is not a PTY name. Killing a terminal does not mean huginn has nothing to
reattach to, unless the runtime itself has forgotten the conversation.

Live vs resumable is a first-class distinction:

- **live** — a process on this machine currently holds the conversation
  (TUI, app-server thread, ACP agent, `claude` with channels up)
- **resumable** — on disk, no live process; huginn may spawn an adapter if
  the caller asks, it does not spawn by default

## The pipe

Five things, and only five:

1. **Discover** live and resumable sessions on this host (runtime, id, cwd,
   title, liveness, how to attach).
2. **Attach** as a structured client without stealing the human's TUI.
3. **Prompt** — inject a user turn into that conversation.
4. **Watch** — stream structured events (assistant text, tool calls, plans,
   permission requests, turn end).
5. **Steer** — interrupt a turn; allow or deny a permission prompt when the
   runtime exposes that.

Proof of who is asking is required for (2)–(5).

Nothing else is in the pipe. Humans attach with ssh or tmux; Huginn does not
wrap that. Network is the operator's overlay.

## Broker contract

JSON-RPC on `POST /`. ACP on `/acp` is the same verbs in ACP names
(`session/list`, `session/load`, `session/prompt`, `session/update`,
`session/cancel`). MCP tools on `/mcp` map 1:1.

```
session/list
session/watch
session/prompt
session/interrupt
session/permission   # allow | deny, only when the adapter advertised it
```

A list row names at least: host, runtime (`grok` | `codex` | `claude`),
session id, cwd, title, live/resumable, adapter, capabilities
(`prompt`, `watch`, `interrupt`, `permission`). `join` is `none` when
nothing on this host can attach (leaderless Grok TUI, Claude without the
channel plugin, Codex without an app-server).

`session/list` filters and pages:

```
{"liveness":"live","runtime":"grok","cwd":"/path/prefix","limit":200,"cursor":"…"}
```

`limit` defaults to 200 and caps at 1000. The result carries `total` and
`nextCursor`. Ask for `{"liveness":"live"}` when you mean what is running
now. An unfiltered list is history.

ACP `session/new` is refused: spawn is not join-live-TUI. `session/load` of
a `join=none` row fails with the same honest error prompt would.

Adapters map:

- Grok → ACP `session/load`, `session/prompt`, `session/update`,
  `session/cancel`, permission requests
- Codex → `thread/list|resume`, `turn/start|steer|interrupt`, item
  notifications, approval requests
- Claude → channel `notifications/claude/channel` (prompt in), reply tool
  (text back), `notifications/claude/channel/permission*` (verdicts).
  Claude channels are inject-into-existing-TUI, not a full ACP peer.

Lossy mappings stay lossy in the type, not hidden.

## Runtime rules

**Grok Build.** Prefer attaching to an already-running pager/leader. Spawn
`grok agent serve` only when nothing live exists and the caller asked to
resume. Do not scrape the TUI.

**Codex.** Prefer a long-lived `codex app-server` on a unix socket (or
loopback websocket with auth). Human TUI connects with `codex --remote`.
Huginn is a second app-server client on the same thread. Stdio app-server is
single-client and is not the attach path. Do not wrap the TUI in a PTY.

**Claude Code.** First path is a huginn **channel plugin**: MCP server with
`claude/channel`. This is not the `/mcp` door above — `huginn-channel` injects
into one live TUI. grokbot talks to the sidecar; the sidecar emits channel
notifications into that session.

This repo’s `.mcp.json` registers `server:huginn`. Team/Enterprise need an
Owner to set `channelsEnabled`; Max/Pro can use the development flag:

```
make build
claude --dangerously-load-development-channels server:huginn
```

Export `HUGINN_TOKEN` (or write `.huginn-token` in the repo root, gitignored)
so the plugin can register with the sidecar.

Do **not** reverse-engineer Remote Control (`claude --remote-control`).
ACP adapters that spawn `claude -p --output-format stream-json` start a
*new* agent process. They are a resume/spawn path, not "join the TUI I
already have open."

## Host sidecar

One process per machine. Loopback by default; `--bind` a private overlay IP
to share. Discovers by reading what the runtimes already write. Does not
keep a durable copy of transcripts. Does not invent a hosted control plane.

Multi-machine is N URLs (one sidecar each).

## Security

Attaching to a live coding agent is equivalent to sitting at that keyboard
for prompts and, if permission relay is on, for tool approval.

- Loopback or private overlay only. No public listener (`0.0.0.0` is refused).
- One secret. Token = full access to this host.
- The Nix wrapper cats `tokenFile` at exec.
- Claude channel path must sender-allowlist.
- Permission relay is opt-in per session.
- Do not log prompt bodies, tool outputs, or file contents.
- Codex app-server websocket off-loopback requires its auth flags.

## Sequencing

Nothing after a step starts until that step has a spike with a real
runtime, not a mock.

1. **Grok ACP** — list, attach to a live leader, prompt, stream updates.
2. **Codex app-server** — unix socket, TUI via `--remote`, huginn as second client.
3. **Claude channel** — huginn MCP channel plugin, loopback inject, allowlist.
4. **Sidecar contract** — one process, five verbs, auth, MCP + ACP doors.
5. **Cross-host** — private overlay (Tailscale / WireGuard), not a bus.
   Default: `--bind $(tailscale ip -4):7419` under launchd/KeepAlive.
   Serve is optional. See INSTALL.md §4 and [#3](https://github.com/pyrex41/huginn/issues/3).

Finite bar for v1: from grokbot, list sessions on one enrolled machine,
watch a live Grok turn, inject a prompt into that turn, inject into a live
Claude TUI via the channel, inject into a Codex thread whose TUI is
`--remote` against the same app-server. If any of those requires keystrokes
into a PTY, v1 has failed.

### Where this actually is

Done: five verbs over loopback HTTP; the three adapters; filtered/paged
`session/list` that is fast on a real Grok home; honest `join=none` when
nothing is attachable; MCP and ACP on the same listener; Nix sidecar module;
cross-host **list** from grokbot over Tailscale IP bind (spike; Serve optional).

Not done, in the order it matters:

1. Dual-client inject into a **leader-backed** Grok TUI, a Claude TUI with
   the channel plugin loaded, and a Codex app-server `--remote` thread.
   List works; those attach paths are still fail-closed on a typical laptop.
2. Per-principal authorization (garmr). Until then the token is host-wide.
3. A `pi` adapter — [#2](https://github.com/pyrex41/huginn/issues/2).

## Refusals

**R1. No PTY, no terminal emulator, no keystroke injection.**
Those are shenmux.

**R2. No reverse-engineered Claude Remote Control.**
Channels for live Claude TUIs.

**R3. It does not create, schedule, or reconcile work.**

**R4. It does not store user content.**
No transcript archive.

**R5. It has no vocabulary for where the process runs beyond this host.**

**R6. It is not a multi-agent orchestrator.**

**R7. It does not ship a phone UI.**
grokbot is the client. A debug CLI is allowed.

## Layout

```
README.md            this document
INSTALL.md           token, serve, overlay bind, harness JSON
cmd/huginn/          sidecar + debug CLI + `huginn acp`
cmd/huginn-channel/  Claude channel plugin (injects into one live TUI)
internal/broker/     the five verbs, MCP, ACP
internal/adapter/    grok, codex, claude — native protocols only
internal/discover/   live vs resumable probes
nix/                 packaging and the sidecar module
```

## Name

Huginn is the raven that flies out and brings back what it saw. The sidecar
sits on a machine, looks at the sessions that are actually there, and
reports. It does not become the thing it is watching.
