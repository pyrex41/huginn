# huginn

A host sidecar so grokbot can attach to live Claude Code, Codex, and Grok
Build sessions as a structured client.

Huginn does not own a PTY, does not paint a terminal, and does not type
keystrokes into a TUI. It discovers agent sessions on a machine, attaches
through each runtime's native control plane, and exposes one small contract
to grokbot: list, watch, prompt, interrupt, permission verdict.

That is the whole product.

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

Proof of who is asking is required for (2)–(5). Discovery over local IPC may
be looser.

Nothing else is in the pipe. If grokbot can do the job without a piece, that
piece does not live here.

## Broker contract

One JSON-RPC (or equivalent) surface that grokbot speaks. Internally
ACP-shaped is the least-wrong common language. Exact methods can move; the
verbs cannot:

```
session/list
session/watch
session/prompt
session/interrupt
session/permission   # allow | deny, only when the adapter advertised it
```

A list row names at least: host, runtime (`grok` | `codex` | `claude`),
session id, cwd, title, live/resumable, adapter, capabilities
(`prompt`, `watch`, `interrupt`, `permission`).

`session/list` filters and pages, because a host accumulates every resumable
conversation its runtimes ever wrote — thousands of rows, of which a handful
are live:

```
{"liveness":"live","runtime":"grok","cwd":"/path/prefix","limit":200,"cursor":"…"}
```

`liveness`, `runtime`, and `cwd` are optional filters; `limit` defaults to 200
and caps at 1000. The result carries `total` (everything matching the filter,
not just this page) and `nextCursor` (empty on the last page). A caller that
ignores both sees a short list, never a silently truncated one. The cursor is
keyset, not an offset, so a session appearing or vanishing mid-walk does not
shift the rows around it.

Ask for `{"liveness":"live"}` when you mean "what is running right now" — that
is a small response on any host. An unfiltered list is history.

Adapters map:

- Grok → ACP `session/new|load`, `session/prompt`, `session/update`,
  `session/cancel`, permission requests
- Codex → `thread/list|resume`, `turn/start|steer|interrupt`, item
  notifications, approval requests
- Claude → channel `notifications/claude/channel` (prompt in), reply tool
  (text back), `notifications/claude/channel/permission*` (verdicts).
  Claude channels are inject-into-existing-TUI, not a full ACP peer.

Lossy mappings stay lossy in the type, not hidden. A Claude channel attach
does not pretend to be `session/load`.

## Runtime rules

**Grok Build.** Prefer attaching to an already-running pager/leader. Spawn
`grok agent serve` only when nothing live exists and the caller asked to
resume. Do not scrape the TUI.

**Codex.** Prefer a long-lived `codex app-server` on a unix socket (or
loopback websocket with auth). Human TUI connects with `codex --remote`.
Huginn is a second app-server client on the same thread. Stdio app-server is
single-client and is not the attach path. Fork Codex only if dual-client on
one live thread is impossible without a patch; the patch stays small and
upstream-shaped. Do not wrap the TUI in a PTY.

**Claude Code.** First path is a huginn **channel plugin**: MCP server with
`claude/channel` (and `claude/channel/permission` once inject works). grokbot
POSTs to the sidecar; the sidecar emits channel notifications into the live
session. Local UDS peer messaging is a later fan-out, not v1.

This repo’s `.mcp.json` registers `server:huginn`. Team/Enterprise need an
Owner to set `channelsEnabled`; Max/Pro can use the development flag:

```
make build
claude --dangerously-load-development-channels server:huginn
```

`/status` must show the huginn MCP server connected, not “no MCP server
configured with that name”. Export `HUGINN_TOKEN` (or write `.huginn-token`
in the repo root, gitignored) so the plugin can register with the sidecar.

Do **not** reverse-engineer Remote Control (`claude --remote-control`, the
Anthropic WebSocket) as a grokbot client. That protocol is for claude.ai and
the Claude app, stores transcripts on Anthropic servers, requires a
claude.ai login, and is not a bot API. Community RC clients will break.

ACP adapters that spawn `claude -p --output-format stream-json` start a
*new* agent process. They are a resume/spawn path, not "join the TUI I
already have open."

## Host sidecar

One process per machine.

- Bound to loopback by default.
- Reachable from grokbot the same way other private host tools are: Tailscale
  or an outbound-only relay the host dials. An optional Tailcat overlay is
  later transport, not a hosted control plane. Huginn does not invent one.
- Discovers by reading what the runtimes already write (Claude session
  registry + UDS, Codex app-server socket, Grok `~/.grok/sessions`) and by
  probing liveness. It does not require the human to advertise names.
- Does not supervise Claude/Codex/Grok as children unless the caller
  explicitly asked to spawn/resume.
- Does not keep a durable copy of transcripts. The runtime already does.

Cross-host "sessions anywhere" is a registry of these sidecars, not a
central session store.

## Cross-host / overlay

The five verbs stay the same. Transport is optional and sits *in front of*
the loopback JSON-RPC server; it is not a sixth verb and it does not join
Claude, Codex, or Grok TUIs.

Prefer a real Tailscale tailnet for always-on enrolled machines. Use
`huginn serve --tailcat` for one-shot / untrusted / no system-network
changes: userspace WireGuard + magicsock + DERP, no Tailscale account, no
TUN, no routing-table edits. The sidecar still binds `127.0.0.1`. Overlay
clients still send `HUGINN_TOKEN` (or `--token`). The printed `tc…`
ConnBlob is a bearer capability to *reach* that socket; without
`--tailcat-allow nodekey:…` (repeatable; same as `tailcat serve --allow`)
anyone who has the blob can dial it.

A grokbot-side client dials the huginn port through the blob, then speaks
the existing JSON-RPC:

```
# sidecar (stderr prints the tc… token; stdout is not JSON-RPC)
HUGINN_TOKEN=… huginn serve --tailcat --tailcat-allow nodekey:…

# client: TCP to the huginn port (default 7419) over the tunnel
tailcat <tc-blob> 7419
# or: tailcat socks <tc-blob> curl -H "Authorization: Bearer $HUGINN_TOKEN" \
#       http://server.tailcat:7419/ …
```

Keys are ephemeral (`--key=new` semantics). Huginn does not silently reuse
a saved default key. The Tailcat Go API has no stability promise; this repo
pins `github.com/tailscale/tailcat` v0.3.0.

## What grokbot gets

grokbot can:

- ask "what is running on the studio Mac / this laptop / that box"
- watch a live turn (tools, text, waiting-for-permission)
- inject a follow-up without sitting in the TUI
- approve or deny a tool prompt when the adapter supports it
- resume a disk session into a live adapter when asked

grokbot cannot:

- become the terminal
- steal an exclusive keyboard lease
- silently auto-approve every tool (permission policy is explicit per
  attach, default deny-until-configured)
- drive a session whose runtime is not installed on that host

## Try the zmqcat mailbox transport

`zmqcat` can own the durable mailbox and Tailcat transport while Huginn runs
as a named READY worker. The existing HTTP API remains available; all methods
except the streaming `session/watch` can also be sent as JSON-RPC request
bodies over ZMQC.

In one terminal, start a local durable bus:

```sh
cd ../zmqcat
go build -o bin/zmqcat ./cmd/zmqcat
bin/zmqcat serve --local --mailbox ./mailbox.json
```

In another terminal, attach Huginn to the default local zmqcat socket:

```sh
cd ../huginn
make build
HUGINN_TOKEN=dev-secret bin/huginn serve --zmqcat --zmqcat-service huginn.local
```

`--zmqcat-workers` (default 4) sets how many requests are served concurrently;
each worker holds its own zmqcat session, because a blocking READY occupies
one and a single worker would queue every caller behind one slow
`session/prompt`.

Then issue a session request through the mailbox:

```sh
../zmqcat/bin/zmqcat req huginn.local \
  '{"jsonrpc":"2.0","id":1,"method":"session/list","params":{}}'
```

For a remote trial, remove `--local` from `zmqcat serve`, run `zmqcat join`
with the printed Tailcat token on the Huginn host, and point Huginn at that
join process's local socket. Huginn itself does not need a Tailcat flag in
this topology.

### Presence

With `--zmqcat`, the sidecar announces itself on `huginn.presence.<service>`
every 15s (`--zmqcat-presence-every`, or `--zmqcat-no-presence` to opt out).
An orchestrator subscribes to the `huginn.presence.` prefix and zmqcat's
last-value cache replays the most recent announcement per machine
immediately, so a roster is available on connect rather than after a full
interval on every host.

The announcement is deliberately cheap — service, host, runtimes, bind,
timestamp. It carries no session counts: presence runs on a timer, and
counting sessions means walking thousands of files. Ask `session/list` with
`{"liveness":"live"}` for that.

### What `--zmqcat` does to the trust boundary

Over HTTP, every caller proves it holds `HUGINN_TOKEN`. Over zmqcat there is
no such proof: the worker attaches the token to the request it hands its own
broker, so **anything that can put a job on the service mailbox gets fully
authenticated Huginn RPC**. zmqcat has no mailbox-level ACLs, and its default
sidecar is a unix socket at `/tmp/zmqcat-<uid>.sock` created with the ordinary
umask.

Enabling `--zmqcat` therefore delegates authentication to whoever controls
that socket and, for the remote topology, to the Tailcat overlay's `--allow`
list. Do not enable it on a host where untrusted local users can reach the
sidecar. The HTTP surface keeps its own token check either way.

## Relationship to other repos

| Repo | Owns | Huginn does with it |
| --- | --- | --- |
| **shenmux** | PTY, screen, exclusive input lease | Nothing. Different pipe. |
| **command-center** / **shen-command-center** | Work engine, harness isolation, take/lease, policy | Huginn is not a control plane and does not schedule work. A later consumer may call huginn. That consumer is not this repo. |
| **garmr** | Capability gateway | Authz for "may this principal prompt session X" can sit in front. Huginn does not reimplement it. |
| Runtime CLIs | Claude / Codex / Grok | Huginn is a client of their protocols. |

If a change would make huginn a multiplexer, a work queue, or a hosted
product, it is in the wrong repository.

## Refusals

Each names the owner instead. Checkable in a PR.

**R1. No PTY, no terminal emulator, no keystroke injection.**
Those are shenmux. A "fallback: type into tmux" adapter is a bug.

**R2. No reverse-engineered Claude Remote Control.**
Channels for live Claude TUIs. Official Agent SDK / print-mode only for
spawn/resume, and labelled as such.

**R3. It does not create, schedule, or reconcile work.**
No jobs, no DAG, no Kubernetes, no "run this task overnight" engine. It
attaches to conversations that already exist, or resumes one the runtime
already stored.

**R4. It does not store user content.**
No transcript archive, no screenshot of a TUI, no object store. Pointers
and liveness only.

**R5. It has no vocabulary for where the process runs beyond this host.**
No Cluster, Pod, Harness, Workspace-as-a-type. Opaque labels if a caller
passes them through. A typed field is a claim huginn understands the
concept. It does not.

**R6. It is not a multi-agent orchestrator.**
No subagent fan-out of its own. If Grok/Codex/Claude spawn children, huginn
may list them when the native protocol does. It does not invent a tree.

**R7. It does not ship a phone UI.**
grokbot is the client. A debug CLI for the sidecar is allowed. A Pixi
terminal is not.

## Security

Attaching to a live coding agent is equivalent to sitting at that keyboard
for prompts and, if permission relay is on, for tool approval.

- Loopback or private overlay only. No public listener.
- Sidecar auth is a secret or device credential, not an unauthenticated
  local port. A Tailcat `tc…` token does not replace `HUGINN_TOKEN`.
- Claude channel path must sender-allowlist. An ungated channel is prompt
  injection into the developer's session.
- Permission relay is opt-in per session. Anyone who can send a verdict can
  approve `Bash` / `Write`.
- Do not log prompt bodies, tool outputs, or file contents. Session ids,
  runtime, cwd, and attach errors are enough.
- Codex app-server websocket off-loopback requires its auth flags. Huginn
  must not start an unauthenticated non-loopback listener.

## Sequencing

Nothing after a step starts until that step has a spike with a real
runtime, not a mock.

1. **Grok ACP** — list `~/.grok/sessions`, attach to a live leader/agent,
   `session/prompt`, stream `session/update`. Prove dual presence: human TUI
   still works while huginn watches and injects.
2. **Codex app-server** — unix socket, TUI via `--remote`, huginn as second
   client on one thread. If dual-client fails, document the gap and only
   then consider an upstream-shaped patch. No PTY fallback.
3. **Claude channel** — huginn MCP channel plugin, loopback inject from the
   sidecar, reply tool, sender allowlist. Development-channels flag is fine
   until allowlisted. Then permission relay.
4. **Sidecar contract** — one process, `session/list` across the three
   adapters, auth on the grokbot socket.
5. **Cross-host** — registry of sidecars over Tailscale or an outbound
   relay (optional Tailcat overlay is transport only). Last. Not a reason
   to build a controller first.

Finite bar for v1: from grokbot, list sessions on one enrolled machine,
watch a live Grok turn, inject a prompt into that turn, inject into a live
Claude TUI via the channel, inject into a Codex thread whose TUI is
`--remote` against the same app-server. If any of those requires keystrokes
into a PTY, v1 has failed.

## Layout (when there is code)

```
README.md          this document; the product is the pipe it describes
cmd/huginn/        sidecar + debug CLI
internal/broker/   the five verbs
internal/overlay/  optional Tailcat transport (not a verb)
internal/adapter/  grok, codex, claude — native protocols only
internal/discover/ live vs resumable probes
```

No web UI package. No deploy chart. No shenmux import.

## Name

Huginn is the raven that flies out and brings back what it saw. The sidecar
sits on a machine, looks at the sessions that are actually there, and
reports. It does not become the thing it is watching.
