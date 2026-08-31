let meta = #{
    name: "build-huginn",
    description: "Build the huginn host sidecar: Grok/Codex/Claude adapters behind five session verbs, no PTY",
    when_to_use: "Implement huginn from README.md. Grok ACP, then Codex app-server, then Claude channel, then the sidecar contract. Do not merge to main.",
    phases: [
        #{ title: "Explore", detail: "one read-only adapter brief per runtime" },
        #{ title: "Scaffold", detail: "Go module, broker stubs, loopback JSON-RPC, commit on a branch" },
        #{ title: "Grok spike", detail: "ACP attach, dual presence, broker tests" },
        #{ title: "Codex spike", detail: "app-server dual-client or documented gap" },
        #{ title: "Claude channel", detail: "MCP channel plugin, allowlist, inject not RC" },
        #{ title: "Sidecar contract", detail: "one process, honest capabilities, Makefile test" },
        #{ title: "Verify", detail: "independent fail-closed reviewers" },
        #{ title: "Fix", detail: "one resume of the last implementer if verify failed" },
    ],
};

let notes_schema = #{
    "type": "object",
    "required": ["notes"],
    "properties": #{
        "notes": #{ "type": "string" },
        "blocked": #{ "type": "string" },
    },
};

let impl_schema = #{
    "type": "object",
    "required": ["status", "summary", "files", "branch", "sha"],
    "properties": #{
        "status": #{ "type": "string", "enum": ["shipped", "blocked", "unknown"] },
        "summary": #{ "type": "string" },
        "files": #{ "type": "array", "items": #{ "type": "string" } },
        "branch": #{ "type": "string" },
        "sha": #{ "type": "string" },
        "proven": #{ "type": "string" },
        "blocked": #{ "type": "string" },
        "pty_used": #{ "type": "boolean" },
        "how_to_run": #{ "type": "string" },
        "pushed": #{ "type": "boolean" },
    },
};

let verdict_schema = #{
    "type": "object",
    "required": ["pass", "summary", "gaps", "evidence"],
    "properties": #{
        "pass": #{ "type": "boolean" },
        "summary": #{ "type": "string" },
        "gaps": #{ "type": "array", "items": #{ "type": "string" } },
        "evidence": #{ "type": "string" },
        "pty_used": #{ "type": "boolean" },
        "command": #{ "type": "string" },
        "exit_code": #{ "type": "string" },
    },
};

fn trimmed(s) {
    if type_of(s) == "string" {
        s.trim();
        s
    } else {
        ""
    }
}

fn contains_str(hay, needle) {
    if type_of(hay) != "string" || type_of(needle) != "string" {
        return false;
    }
    let n = needle.len();
    let h = hay.len();
    if n == 0 {
        return true;
    }
    if n > h {
        return false;
    }
    let i = 0;
    while i + n <= h {
        if hay.sub_string(i, n) == needle {
            return true;
        }
        i += 1;
    }
    false
}

fn output_map(r) {
    if r == () || r.success != true || r.output == () {
        return ();
    }
    if type_of(r.output) == "map" {
        return r.output;
    }
    ()
}

fn str_field(o, fallback) {
    if o == () {
        return fallback;
    }
    if type_of(o) == "string" {
        let t = trimmed(o);
        if t == "" {
            return fallback;
        }
        return t;
    }
    fallback
}

fn notes_of(r) {
    let o = output_map(r);
    if o == () || o.notes == () {
        return "";
    }
    trimmed(o.notes)
}

fn summary_of(r) {
    let o = output_map(r);
    if o == () || o.summary == () {
        return "";
    }
    trimmed(o.summary)
}

fn branch_of(r) {
    let o = output_map(r);
    if o == () || o.branch == () {
        return "";
    }
    trimmed(o.branch)
}

fn sha_of(r) {
    let o = output_map(r);
    if o == () || o.sha == () {
        return "";
    }
    trimmed(o.sha)
}

fn blocked_of(r) {
    let o = output_map(r);
    if o == () || o.blocked == () {
        return "";
    }
    trimmed(o.blocked)
}

fn proven_of(r) {
    let o = output_map(r);
    if o == () || o.proven == () {
        return "";
    }
    trimmed(o.proven)
}

fn files_of(r) {
    let o = output_map(r);
    if o == () || o.files == () || type_of(o.files) != "array" {
        return [];
    }
    o.files
}

fn pty_of(r) {
    let o = output_map(r);
    if o == () {
        return false;
    }
    o.pty_used == true
}

fn agent_ok(r) {
    r != () && r.success == true
}

fn bool_str(b) {
    if b == true {
        "true"
    } else {
        "false"
    }
}

fn id_of(r) {
    if r == () || r.agent_id == () {
        return "";
    }
    if type_of(r.agent_id) == "string" {
        return r.agent_id;
    }
    ""
}

fn path_forbidden(p) {
    let s = trimmed(p);
    if s == "" {
        return false;
    }
    if contains_str(s, "shenmux") {
        return true;
    }
    if contains_str(s, "shen-command-center") {
        return true;
    }
    false
}

fn files_violate(files) {
    if files == () || type_of(files) != "array" {
        return false;
    }
    for p in files {
        if path_forbidden(p) {
            return true;
        }
    }
    false
}

fn law_text(repo) {
    let s = "";
    s += "AUTHORITY: read ";
    s += repo;
    s += "/README.md with the read_file tool BEFORE doing anything else. ";
    s += "If this prompt and the README fight, the README wins. ";
    s += "You do not inherit any parent conversation. Use tools; do not answer from memory.\n\n";
    s += "REPO: ";
    s += repo;
    s += " (git@github.com:pyrex41/huginn.git, private). Edit only this repo. ";
    s += "Language: Go, tests in-repo, loopback + token auth, default deny on permission relay.\n\n";
    s += "PRODUCT: a host sidecar so grokbot can attach to live Claude Code, Codex, and Grok Build ";
    s += "sessions as a structured client. Five verbs only: session/list, session/watch, ";
    s += "session/prompt, session/interrupt, session/permission. Internally ACP-shaped.\n\n";
    s += "HARD REFUSALS (a violation is a failed task, not a stretch goal):\n";
    s += "R1 no PTY, no terminal emulator, no tmux/shenmux/keystroke injection.\n";
    s += "R2 no reverse-engineered Claude Remote Control / Anthropic session WebSocket.\n";
    s += "R3 no work queue, DAG, k8s, overnight job engine.\n";
    s += "R4 no transcript/object store.\n";
    s += "R5 no Cluster/Pod/Harness types.\n";
    s += "R6 no invented subagent tree.\n";
    s += "R7 no phone/web UI.\n";
    s += "Do not import or depend on shenmux. Do not edit any repo except huginn.\n";
    s
}

fn worktree_handoff(branch, sha) {
    let s = "";
    s += "WORKTREE: you run in a private git worktree. Parent HEAD may still be README-only. ";
    s += "Worktrees share refs: after you commit, later agents can check out the same branch.\n";
    s += "1. pwd; confirm you are in a huginn worktree, not another project.\n";
    s += "2. Feature branch name: feat/build-huginn (create on first write; otherwise check it out).\n";
    if branch != "" {
        s += "Previous branch: ";
        s += branch;
        s += "\n";
    }
    if sha != "" {
        s += "Previous SHA: ";
        s += sha;
        s += " — git checkout that branch/SHA before editing. Do not start from empty main if this SHA exists.\n";
    }
    s += "3. Commit on feat/build-huginn. Do not merge to main. Do not amend other people's history.\n";
    s += "4. Push if ssh/gh works: git push -u origin feat/build-huginn. If push fails, still return the local SHA.\n";
    s += "5. Isolation does not merge into the parent workspace; the branch/SHA IS the handoff.\n";
    s
}

let repo = "/Users/reuben/projects/huginn";
if args != () && args.repo != () && type_of(args.repo) == "string" {
    let given = trimmed(args.repo);
    if given != "" {
        repo = given;
    }
}

let law = law_text(repo);
let default_branch = "feat/build-huginn";

phase("Explore");
log("Exploring Grok ACP, Codex app-server, and Claude channels");

let grok_explore = "";
grok_explore += law;
grok_explore += "TASK: write a concrete Grok Build adapter brief. Use tools: web_search, web_fetch, ";
grok_explore += "read_file, grep, run_terminal_command (read-only: grok --help, grok agent --help, ls ~/.grok/sessions). ";
grok_explore += "Do not write files.\n";
grok_explore += "Cover: discover live vs resumable under ~/.grok/sessions; attach to an already-running ";
grok_explore += "human pager/leader without stealing the TUI; session/prompt; stream session/update; ";
grok_explore += "interrupt; permission requests. Cite real flags, RPCs, and paths. Prefer attaching to a ";
grok_explore += "live leader; spawn grok agent serve only when nothing live exists AND the caller asked to resume. ";
grok_explore += "Empty notes only after you tried the tools. Return notes as the full brief.";

let codex_explore = "";
codex_explore += law;
codex_explore += "TASK: write a concrete Codex adapter brief. Use tools: web_search, web_fetch, ";
codex_explore += "read_file, grep, run_terminal_command (codex --help, docs, github.com/openai/codex app-server). ";
codex_explore += "Do not write files.\n";
codex_explore += "Cover: long-lived app-server on a unix socket (or loopback websocket WITH auth); ";
codex_explore += "human TUI via codex --remote; huginn as a second client on the same thread; ";
codex_explore += "thread/list|resume, turn/start|steer|interrupt, item notifications, approval requests. ";
codex_explore += "Stdio app-server is single-client and is NOT the attach path. ";
codex_explore += "If dual-client on one live thread is impossible, say so with the exact RPC/error you observed or documented — ";
codex_explore += "do not invent a PTY fallback. Empty notes only after you tried. Return notes as the full brief.";

let claude_explore = "";
claude_explore += law;
claude_explore += "TASK: write a concrete Claude Code adapter brief. Use tools: web_search, web_fetch, ";
claude_explore += "read_file, grep, run_terminal_command (claude --help, MCP channel docs). Do not write files.\n";
claude_explore += "Live join path is a huginn MCP channel plugin: notifications/claude/channel (prompt in), ";
claude_explore += "reply tool (text back), sender allowlist, then claude/channel/permission once inject works. ";
claude_explore += "Development-channels flag is fine until allowlisted. Local UDS peers are later fan-out, not v1. ";
claude_explore += "FORBIDDEN: claude --remote-control and any reverse-engineered Anthropic session WebSocket. ";
claude_explore += "claude -p / ACP spawn / Agent SDK print-mode is resume/spawn, not join-live-TUI — label it that way. ";
claude_explore += "Empty notes only after you tried. Return notes as the full brief.";

let explores = parallel([
    #{
        prompt: grok_explore,
        label: "explore:grok",
        capability_mode: "read-only",
        output_schema: notes_schema,
    },
    #{
        prompt: codex_explore,
        label: "explore:codex",
        capability_mode: "read-only",
        output_schema: notes_schema,
    },
    #{
        prompt: claude_explore,
        label: "explore:claude",
        capability_mode: "read-only",
        output_schema: notes_schema,
    },
]);

let grok_notes = "";
let codex_notes = "";
let claude_notes = "";
let ei = 0;
for r in explores {
    let n = notes_of(r);
    if n == "" {
        log("explore slot " + ei.to_string() + " produced no notes");
    }
    if ei == 0 {
        grok_notes = n;
    }
    if ei == 1 {
        codex_notes = n;
    }
    if ei == 2 {
        claude_notes = n;
    }
    ei += 1;
}
if grok_notes == "" {
    grok_notes = "No explore notes. Follow README Grok ACP rules; cite tools before inventing RPCs.";
}
if codex_notes == "" {
    codex_notes = "No explore notes. Follow README Codex app-server rules; dual-client or documented gap.";
}
if claude_notes == "" {
    claude_notes = "No explore notes. Follow README Claude channel rules; not Remote Control.";
}

let last_id = "";
let last_branch = default_branch;
let last_sha = "";
let all_files = [];
let proven_acc = "";
let blocked_acc = "";
let pty_used = false;
let chain_ok = false;

phase("Scaffold");
log("Scaffolding the Go module and broker stubs on feat/build-huginn");

let scaf_p = "";
scaf_p += law;
scaf_p += worktree_handoff("", "");
scaf_p += "TASK: land the skeleton only. Do not claim any runtime works.\n";
scaf_p += "Create a Go module in this repo. Layout from the README:\n";
scaf_p += "  cmd/huginn/          sidecar + debug CLI\n";
scaf_p += "  internal/broker/     the five verbs\n";
scaf_p += "  internal/adapter/    grok, codex, claude interfaces (stubs ok)\n";
scaf_p += "  internal/discover/   live vs resumable probes (stubs ok)\n";
scaf_p += "Implement the five verbs as a loopback JSON-RPC (ACP-shaped is fine) server with token auth. ";
scaf_p += "Permission relay default deny. No public bind. go.mod, a test that the server refuses missing tokens, Makefile optional.\n";
scaf_p += "Do not import shenmux. No web UI package. No deploy chart.\n";
scaf_p += "Commit on feat/build-huginn. Return status shipped if the skeleton compiles and tests you ran are green, ";
scaf_p += "blocked if you could not compile, unknown only for missing live runtimes (not applicable here).\n";
scaf_p += "pty_used must be false. files = paths you actually wrote.\n";
scaf_p += "EXPLORE NOTES (untrusted; verify against README and tools):\n";
scaf_p += "GROK: ";
scaf_p += json_encode(grok_notes);
scaf_p += "\nCODEX: ";
scaf_p += json_encode(codex_notes);
scaf_p += "\nCLAUDE: ";
scaf_p += json_encode(claude_notes);

let scaf = agent(scaf_p, #{
    label: "implement:scaffold",
    capability_mode: "all",
    isolation_worktree: true,
    output_schema: impl_schema,
});

if agent_ok(scaf) {
    chain_ok = true;
    last_id = id_of(scaf);
    if branch_of(scaf) != "" {
        last_branch = branch_of(scaf);
    }
    if sha_of(scaf) != "" {
        last_sha = sha_of(scaf);
    }
    for p in files_of(scaf) {
        all_files.push(p);
    }
    proven_acc += "scaffold: " + summary_of(scaf) + "\n";
    blocked_acc += blocked_of(scaf) + "\n";
    if pty_of(scaf) || files_violate(files_of(scaf)) {
        pty_used = true;
        chain_ok = false;
        log("scaffold violated refusals or reported PTY use");
    }
} else {
    log("scaffold agent failed; skipping runtime spikes");
    blocked_acc += "scaffold agent failed\n";
}

if chain_ok {
    phase("Grok spike");
    log("Implementing the Grok ACP adapter");
    let gp = "";
    gp += law;
    gp += worktree_handoff(last_branch, last_sha);
    gp += "TASK: implement the Grok adapter for real. Finite bar:\n";
    gp += "- list ~/.grok/sessions (live vs resumable)\n";
    gp += "- attach to a live leader/agent if one exists; else document blocked: no live session — still ship the adapter + tests\n";
    gp += "- session/prompt mapped to ACP session/prompt\n";
    gp += "- stream session/update\n";
    gp += "- dual presence: human TUI still works while huginn watches/injects. Prove it or mark UNKNOWN with what you tried.\n";
    gp += "Prefer already-running pager/leader. Spawn grok agent serve only when nothing live exists AND resume was asked.\n";
    gp += "Tests for the broker mapping even if live attach is environment-blocked. Commit. Do not merge to main.\n";
    gp += "EXPLORE NOTES (untrusted):\n";
    gp += json_encode(grok_notes);
    let gr = agent(gp, #{
        label: "implement:grok",
        capability_mode: "all",
        isolation_worktree: true,
        output_schema: impl_schema,
    });
    if agent_ok(gr) {
        last_id = id_of(gr);
        if branch_of(gr) != "" { last_branch = branch_of(gr); }
        if sha_of(gr) != "" { last_sha = sha_of(gr); }
        for p in files_of(gr) { all_files.push(p); }
        proven_acc += "grok: " + summary_of(gr) + " | " + proven_of(gr) + "\n";
        blocked_acc += blocked_of(gr) + "\n";
        if pty_of(gr) || files_violate(files_of(gr)) {
            pty_used = true;
            chain_ok = false;
            log("grok spike violated refusals");
        }
    } else {
        chain_ok = false;
        log("grok spike agent failed");
        blocked_acc += "grok spike agent failed\n";
    }
}

if chain_ok {
    phase("Codex spike");
    log("Implementing the Codex app-server adapter");
    let cp = "";
    cp += law;
    cp += worktree_handoff(last_branch, last_sha);
    cp += "TASK: implement the Codex adapter. Long-lived app-server on a unix socket; huginn is the second client; ";
    cp += "TUI path is codex --remote against that same server. Stdio app-server is not the attach path.\n";
    cp += "If dual-client on one live thread fails, document the gap with the exact RPC/error. ";
    cp += "Do not PTY-fallback. Do not silently spawn a private app-server and call it joined the TUI. ";
    cp += "Small upstream-shaped patch notes only if dual-client is impossible. ";
    cp += "If Codex is not installed, status unknown, still ship code + tests. Commit. Do not merge to main.\n";
    cp += "EXPLORE NOTES (untrusted):\n";
    cp += json_encode(codex_notes);
    let cr = agent(cp, #{
        label: "implement:codex",
        capability_mode: "all",
        isolation_worktree: true,
        output_schema: impl_schema,
    });
    if agent_ok(cr) {
        last_id = id_of(cr);
        if branch_of(cr) != "" { last_branch = branch_of(cr); }
        if sha_of(cr) != "" { last_sha = sha_of(cr); }
        for p in files_of(cr) { all_files.push(p); }
        proven_acc += "codex: " + summary_of(cr) + " | " + proven_of(cr) + "\n";
        blocked_acc += blocked_of(cr) + "\n";
        if pty_of(cr) || files_violate(files_of(cr)) {
            pty_used = true;
            chain_ok = false;
            log("codex spike violated refusals");
        }
    } else {
        chain_ok = false;
        log("codex spike agent failed");
        blocked_acc += "codex spike agent failed\n";
    }
}

if chain_ok {
    phase("Claude channel");
    log("Implementing the Claude MCP channel plugin");
    let lp = "";
    lp += law;
    lp += worktree_handoff(last_branch, last_sha);
    lp += "TASK: huginn MCP channel plugin. claude/channel, reply tool, sender allowlist, loopback inject from the sidecar. ";
    lp += "Development-channels flag is fine. Permission relay only after inject works; default deny. ";
    lp += "Label any claude -p / ACP-spawn / Agent SDK print-mode path as resume/spawn, not join-live-TUI. ";
    lp += "Do not reverse-engineer Remote Control. If Claude is not installed, status unknown, still ship code + tests. ";
    lp += "Commit. Do not merge to main.\n";
    lp += "EXPLORE NOTES (untrusted):\n";
    lp += json_encode(claude_notes);
    let lr = agent(lp, #{
        label: "implement:claude",
        capability_mode: "all",
        isolation_worktree: true,
        output_schema: impl_schema,
    });
    if agent_ok(lr) {
        last_id = id_of(lr);
        if branch_of(lr) != "" { last_branch = branch_of(lr); }
        if sha_of(lr) != "" { last_sha = sha_of(lr); }
        for p in files_of(lr) { all_files.push(p); }
        proven_acc += "claude: " + summary_of(lr) + " | " + proven_of(lr) + "\n";
        blocked_acc += blocked_of(lr) + "\n";
        if pty_of(lr) || files_violate(files_of(lr)) {
            pty_used = true;
            chain_ok = false;
            log("claude channel violated refusals");
        }
    } else {
        chain_ok = false;
        log("claude channel agent failed");
        blocked_acc += "claude channel agent failed\n";
    }
}

if chain_ok {
    phase("Sidecar contract");
    log("Wiring one process across the three adapters");
    let sp = "";
    sp += law;
    sp += worktree_handoff(last_branch, last_sha);
    sp += "TASK: one process. session/list across grok/codex/claude adapters. Auth on the grokbot socket. ";
    sp += "List rows include host, runtime, session id, cwd, title, live/resumable, adapter, capabilities. ";
    sp += "Advertise honestly: a Claude channel attach is NOT session/load. Debug CLI ok. Makefile test target. ";
    sp += "Loopback default, token auth, no public listener, do not log prompt bodies. Commit. Do not merge to main. Push if possible.\n";
    sp += "v1 bar to aim at (do not fake it): from a client of the sidecar, list sessions on this machine, ";
    sp += "watch a live Grok turn, inject a prompt into it, inject into a live Claude TUI via the channel, ";
    sp += "inject into a Codex thread whose TUI is --remote against the same app-server. ";
    sp += "If a runtime is missing, UNKNOWN that adapter and still ship. If any path used keystrokes into a PTY, the bar failed.";
    let sr = agent(sp, #{
        label: "implement:sidecar",
        capability_mode: "all",
        isolation_worktree: true,
        output_schema: impl_schema,
    });
    if agent_ok(sr) {
        last_id = id_of(sr);
        if branch_of(sr) != "" { last_branch = branch_of(sr); }
        if sha_of(sr) != "" { last_sha = sha_of(sr); }
        for p in files_of(sr) { all_files.push(p); }
        proven_acc += "sidecar: " + summary_of(sr) + " | " + proven_of(sr) + "\n";
        blocked_acc += blocked_of(sr) + "\n";
        if pty_of(sr) || files_violate(files_of(sr)) {
            pty_used = true;
            log("sidecar contract violated refusals");
        }
    } else {
        log("sidecar contract agent failed");
        blocked_acc += "sidecar contract agent failed\n";
    }
}

phase("Verify");
log("Independent fail-closed review of " + last_branch + " @" + last_sha);

let inspect = "";
inspect += "INSPECT git at repo ";
inspect += repo;
inspect += " branch ";
inspect += last_branch;
inspect += " sha ";
inspect += last_sha;
inspect += ". Parent workspace may still be README-only. Use git show, git diff main...<sha>, git ls-tree -r --name-only <sha>. ";
inspect += "To run tests without editing parent: git worktree add a disposable dir at that SHA, run make test or go test ./..., quote command + exit code, then you may remove the disposable worktree. ";
inspect += "Do not edit source. Do not commit. Do not merge.\n";

let v_scope = "";
v_scope += law;
v_scope += inspect;
v_scope += "TASK: scope leak hunt. pass=true only if you read the tree at that SHA and found no PTY/tmux/shenmux/keystroke injection, ";
v_scope += "no Claude Remote Control / Anthropic session WebSocket client, no work-queue/DAG/k8s, no transcript store, ";
v_scope += "no Cluster/Pod/Harness types, no invented subagent tree, no phone/web UI, no shenmux import. ";
v_scope += "Any leak => pass=false and list gaps. Missing tree/SHA => pass=false, evidence empty is a fail. pty_used=true if any path types into a PTY.";

let v_grok = "";
v_grok += law;
v_grok += inspect;
v_grok += "TASK: grok dual-presence evidence. pass=true only with concrete evidence you independently inspected that huginn can list ~/.grok/sessions, ";
v_grok += "attach without stealing the TUI, prompt, and stream session/update — OR an explicit UNKNOWN with the command you ran proving no live session, ";
v_grok += "AND tests for the broker mapping actually ran. Quote command + exit code. A missing/failed test run is pass=false. ";
v_grok += "Do not trust the implementer transcript.";

let v_codex = "";
v_codex += law;
v_codex += inspect;
v_codex += "TASK: Codex dual-client evidence. pass=true only if huginn is a second client on a long-lived app-server unix socket ";
v_codex += "and the TUI path is codex --remote, with evidence — OR an explicit documented gap quoting the exact RPC/error, ";
v_codex += "and code+tests still exist. Spawning a private app-server and calling it join-live-TUI is pass=false. ";
v_codex += "PTY fallback is pass=false. Missing tests is pass=false. Quote command + exit code.";

let v_claude = "";
v_claude += law;
v_claude += inspect;
v_claude += "TASK: Claude channel inject evidence. pass=true only if the live path is an MCP claude/channel plugin with sender allowlist ";
v_claude += "and loopback inject — not Remote Control, not an unlabelled claude -p spawn pretending to join the TUI. ";
v_claude += "Permission relay default deny. Missing runtime may be UNKNOWN with evidence of the probe, but the plugin code must exist. ";
v_claude += "Quote tests command + exit code. RC usage => pass=false and pty_used or gaps as appropriate.";

let v_tests = "";
v_tests += law;
v_tests += inspect;
v_tests += "TASK: run the tests. make test and/or go test ./... at that SHA. pass=true only if you ran them and they exited 0. ";
v_tests += "Put the exact command in command and the numeric exit code in exit_code. evidence must quote a slice of the output. ";
v_tests += "Did not run => pass=false. Do not edit source to make tests pass.";

let verdicts = parallel([
    #{
        prompt: v_scope,
        label: "verify:scope",
        capability_mode: "execute",
        isolation_worktree: true,
        output_schema: verdict_schema,
    },
    #{
        prompt: v_grok,
        label: "verify:grok",
        capability_mode: "execute",
        isolation_worktree: true,
        output_schema: verdict_schema,
    },
    #{
        prompt: v_codex,
        label: "verify:codex",
        capability_mode: "execute",
        isolation_worktree: true,
        output_schema: verdict_schema,
    },
    #{
        prompt: v_claude,
        label: "verify:claude",
        capability_mode: "execute",
        isolation_worktree: true,
        output_schema: verdict_schema,
    },
    #{
        prompt: v_tests,
        label: "verify:tests",
        capability_mode: "execute",
        isolation_worktree: true,
        output_schema: verdict_schema,
    },
]);

let verify_pass = true;
let verify_gaps = [];
let verify_bits = "";
let vi = 0;
let labels = ["scope", "grok", "codex", "claude", "tests"];
for v in verdicts {
    let name = "slot" + vi.to_string();
    if vi < labels.len() {
        name = labels[vi];
    }
    let o = output_map(v);
    if o == () {
        verify_pass = false;
        verify_gaps.push(name + ": missing/failed reviewer slot = unverified");
        log("verify " + name + " unverified");
    } else {
        if o.pass != true {
            verify_pass = false;
        }
        if o.pty_used == true {
            pty_used = true;
            verify_pass = false;
        }
        let ev = str_field(o.evidence, "");
        if ev == "" && o.pass == true {
            verify_pass = false;
            verify_gaps.push(name + ": pass without evidence");
        }
        verify_bits += name + ": pass=" + bool_str(o.pass == true) + " " + str_field(o.summary, "") + "\n";
        if o.gaps != () && type_of(o.gaps) == "array" {
            for g in o.gaps {
                if type_of(g) == "string" {
                    verify_gaps.push(name + ": " + g);
                }
            }
        }
        if o.command != () && o.exit_code != () {
            verify_bits += "  cmd " + str_field(o.command, "") + " exit " + str_field(o.exit_code, "") + "\n";
        }
    }
    vi += 1;
}

if pty_used {
    verify_pass = false;
    verify_gaps.push("PTY/keystroke path used — v1 bar failed");
}

if !verify_pass && last_id != "" {
    phase("Fix");
    log("Verify failed; one resume of the last implementer");
    let fp = "";
    fp += law;
    fp += worktree_handoff(last_branch, last_sha);
    fp += "Verify failed. Fix the gaps. Re-run tests. Do not merge to main. Do not add PTY/RC/shenmux. ";
    fp += "If a runtime is not installed, keep that adapter UNKNOWN and still ship code + tests.\nGAPS:\n";
    fp += json_encode(verify_gaps);
    fp += "\nVERDICTS:\n";
    fp += json_encode(verify_bits);
    let fix_opts = #{
        label: "fix:last-implementer",
        capability_mode: "all",
        isolation_worktree: true,
        output_schema: impl_schema,
        resume_from: last_id,
    };
    let fixed = agent(fp, fix_opts);
    if agent_ok(fixed) {
        if branch_of(fixed) != "" { last_branch = branch_of(fixed); }
        if sha_of(fixed) != "" { last_sha = sha_of(fixed); }
        for p in files_of(fixed) { all_files.push(p); }
        proven_acc += "fix: " + summary_of(fixed) + "\n";
        blocked_acc += blocked_of(fixed) + "\n";
        if pty_of(fixed) || files_violate(files_of(fixed)) {
            pty_used = true;
        }
    } else {
        log("fixer did not succeed");
        blocked_acc += "fix agent failed\n";
    }
}

let v1 = "UNKNOWN";
if pty_used {
    v1 = "failed";
} else {
    if verify_pass {
        v1 = "passed";
    }
}

let report = "";
report += "# build-huginn\n\n";
report += "- branch: " + last_branch + "\n";
report += "- sha: " + last_sha + "\n";
report += "- verify_pass: " + bool_str(verify_pass) + "\n";
report += "- v1_bar: " + v1 + "\n";
report += "- pty_used: " + bool_str(pty_used) + "\n\n";
report += "## Proven\n\n";
report += proven_acc;
report += "\n## Blocked / UNKNOWN\n\n";
report += blocked_acc;
report += "\n## Verify\n\n";
report += verify_bits;
report += "\n## Gaps\n\n";
for g in verify_gaps {
    report += "- " + g + "\n";
}
report += "\n## Files (agent-reported, untrusted)\n\n";
for p in all_files {
    report += "- " + p + "\n";
}

let report_path = write_scratch_file("report.md", report);

complete(#{
    path: report_path,
    report: report,
    branch: last_branch,
    sha: last_sha,
    verify_pass: verify_pass,
    v1_bar: v1,
    pty_used: pty_used,
    files: all_files,
    proven: proven_acc,
    blocked: blocked_acc,
});
