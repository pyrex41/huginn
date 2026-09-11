# Finite-bar spike 2026-09-10 — Mac.lan

Sidecar: `/Users/reuben/projects/huginn/bin/huginn serve --bind 127.0.0.1:7419` (pid 61815). Binary dated 2026-09-09 (working-tree build; session verbs only used).
Runtimes: grok 1.0.25, Claude Code 2.1.266, codex-cli 0.153.4.
Host: Mac.lan. Date: 2026-09-10T18:52:12Z.

## List

`huginn list --liveness live` returned total 61 (first page 50). Adapters grok/codex/claude all `ok`.

`huginn list --liveness live --runtime grok` (4 rows):

| id | join | adapter | cwd |
| --- | --- | --- | --- |
| 01a087ef-1253-7603-8215-318a6de43a11 | none | grok-acp-none | /Users/reuben/projects/huginn |
| 01a0880e-e39e-7b40-8df3-d73522d94148 | none | grok-acp-none | /Users/reuben/projects/shencheck |
| 01a08c16-0b66-7071-9fc2-3f2e8d198cf9 | none | grok-acp-none | /Users/reuben/fg |
| 01a08c9e-4b94-7672-8610-962c41edc720 | none | grok-acp-none | /Users/reuben/fg |

Claude live: `32b40a1a-6e94-41fc-989b-49d0483438c8` join=none adapter=`claude-channel-unattached` cwd=/Users/reuben/fg (`claude --dangerously-skip-permissions -r`, pid 18364).

Codex: dozens of `liveness=live` rows with adapter `codex-app-server-foreign` and empty capabilities (no huginn-owned app-server socket). Not a `--remote` attach.

List latency ~2–3 minutes. `grok leader list --json` hung past 3s with empty stdout. `~/.grok/sessions` has 1881 `summary.json` files.

## Prompts (HTTP JSON-RPC)

Grok live `01a087ef-…` (this TUI):

```
{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"blocked: live session has no leader (attach=none)"}}
```

Claude live `32b40a1a-…`:

```
{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"claude: channel plugin not registered; launch with --dangerously-load-development-channels server:huginn"}}
```

Human TUIs still alive after those calls (`kill -0` on grok 81734 and claude 18364). No PTY wrap.

## Not proven

- Watch + inject into a **leader-backed** Grok turn (no `grok agent leader`; TUIs are leaderless). Did not start a default leader: loading this live session onto a new leader while the TUI holds it would split the conversation.
- Inject into a Claude TUI that actually spawned `huginn-channel`.
- Codex `codex --remote` dual-client on one live turn (no app-server listen socket).
- Isolated adapter live tests: Go 1.27 / downloaded 1.26.5 toolchains missing std packages (`package runtime is not in std`). Concurrent sessions had wiped `~/Library/Caches/go-build` and `~/go/pkg`.
- Two-machine `zmqcat req` (one host).

## Commands

```
HUGINN_TOKEN=… huginn serve --bind 127.0.0.1:7419
huginn list --liveness live --runtime grok --limit 20
huginn rpc session/prompt '{"sessionId":"01a087ef-1253-7603-8215-318a6de43a11","prompt":[{"type":"text","text":"ping from huginn spike"}]}'
huginn rpc session/prompt '{"sessionId":"32b40a1a-6e94-41fc-989b-49d0483438c8","prompt":[{"type":"text","text":"ping channel"}]}'
```
