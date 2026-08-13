# Code Mode / Programmatic Tool Calling: Feasibility Assessment

> **This document assesses work that does not exist.** Nothing described here is implemented in Autoto. Unlike the rest of `docs/`, which maps current behavior, every "Autoto would" or "the program would" below is hypothetical. The only existing-behavior claims are those citing a concrete `file:line`; treat everything else as proposal. The verdict is **against building the full feature**, so most of this document exists to justify not doing something.

Assessed: 2026-08-13. Reference implementation studied: DeepSeek Harness (a TypeScript agent harness with Code Mode shipped), read at `C:\Users\Ray\Desktop\dsh-study`. All `packages/…` and `.agents/…` paths below are relative to that clone; all `internal/…` and `docs/…` paths are relative to this repository.

## 1. Verdict

**Do not build Code Mode in Autoto. Build the structured tool result seam it depends on, because that seam pays for itself independently, and then stop.**

Three findings drive this, in descending order of weight:

1. **Autoto already ships two of the three benefits.** `internal/toolpipeline/manager.go` already captures full tool output and gives the model a ~100-character preview plus an alias, then reduces the captures through a restricted operator rule at the end (`internal/toolpipeline/manager.go:242-286`, `internal/toolpipeline/manager.go:156-240`). `internal/agent/tool_prefetch.go:51-105` already overlaps independent read-risk calls inside one model turn. Subagents already keep an entire work stream out of the parent context at a hard 4096-byte report boundary (`internal/background/agent.go:26`). What is genuinely missing is **control flow** — looping over discovered items, branching on an intermediate value, and deriving call *N+1*'s arguments from call *N*'s result. That is a real gap but a narrower one than the feature's framing suggests.

2. **The prerequisite is blocking, and the reference implementation proves it the hard way.** `tools.Result` carries `Output string` plus a telemetry `Meta` map and nothing else (`internal/tools/tool.go:91-95`). Every tool renders prose. A program calling a projected `Grep` would receive text and have to re-parse it. DeepSeek Harness shipped Code Mode in exactly that state and had to follow it with a separate mandatory canonical-output refactor across every tool, with no compatibility path, because "programs had to scrape job ids and dynamic mount ids from prose" (`.agents/notes/implemented/feature/2026-07-20-code-mode-typed-tool-returns.md:9`; `.agents/notes/implemented/architecture/2026-07-20-canonical-tool-output-contract.md:15-25`).

3. **Every Go runtime option is materially worse than the Node worker thread the reference implementation gets for free, and Windows makes the cheapest option the worst one.** See §5. The one option with real isolation (`wazero`) requires embedding a language interpreter compiled to WASM; the one option that would take a week (shell out to Node) creates Autoto's first exec path that `internal/tools/command_facts.go` cannot statically classify, which is precisely the condition `CommandFacts.Unclassified()` exists to force approval on (`internal/tools/command_facts.go:41-49`).

The cheaper thing that captures most of the benefit is in §9: structured results, then a batch-read tool and a structured selection operator in the pipeline that already exists. Roughly a tenth of the cost, no new runtime, no new trust boundary, and no change to the approval model.

## 2. What Code Mode / PTC is

Instead of the model emitting one tool call per step and each result re-entering its context on the next request, the model writes one program that calls tools in a loop, processes their output, and returns only a conclusion. Intermediate results never enter model context. This removes round-trips and lets the model express control flow and sequential dependency between calls.

Two flavors exist:

- **Anthropic's Programmatic Tool Calling** is server-side. The model writes Python, it runs in Anthropic's execution container, tools opt in through an `allowed_callers` field, and the API pauses to ask the client for each tool result. It requires a beta header. Autoto's provider layer is multi-vendor by design (`docs/ARCHITECTURE.md` §5 lists five adapter families), so a single-vendor server-side mechanism cannot be the primary path — it would work only for the Anthropic adapter and leave every other provider without the feature.
- **Cloudflare's Code Mode**, which DeepSeek Harness implements, is client-side. Tools are projected into a generated TypeScript SDK, the model calls one reserved `run_code` tool, and the harness runs the program locally.

Only the client-side flavor is assessable as an Autoto feature, so the rest of this document means that one.

## 3. What the reference implementation actually does

Worth reading in full before deciding: `.agents/notes/implemented/feature/2026-06-15-code-mode.md`.

**Presentation is a registry mode.** The tool registry gains a validated `mode: 'native' | 'code' | 'both'`, default `'native'`. Under `'code'` the registry contributes only the reserved `run_code` transport plus a generated SDK `.d.ts` in the system prompt (`2026-06-15-code-mode.md:21`, `:31`). `jsonSchemaToTs()` maps the tool JSON-Schema subset to TypeScript, carries schema descriptions into JSDoc, and degrades unsupported constructs to `unknown` (`:39`; `packages/core/tools/src/ts-types.ts:234-245`). Tools appear as quoted object keys so arbitrary names need no aliasing (`:117`).

**Execution is a capability seam, not a hardcoded backend.** `packages/code-runtime/code-runtime/` defines *what* a code runtime does — run one program against named async bindings and report `{ value, logs, error? }` — without saying how (`packages/code-runtime/code-runtime/README.md:5`). Two readonly descriptors, `language` and `isolation`, are informational; the README states outright that `isolation` is "**not a security claim**" (`README.md:15`).

**The shipped backend is one fresh Node worker thread per run.** Host-side TypeScript type-strip first, so a syntax failure never spawns a worker; then a worker with a truly empty `env: {}`, `resourceLimits`, and captured stdout/stderr; the program becomes the body of an `AsyncFunction` so top-level `await` and `return` work; bindings bridge over the message port with the peer treated as hostile (`2026-06-15-code-mode.md:75-78`). Defaults: `computeMs` 60s, `maxWallMs` 600s, `maxOutputBytes` 64 MiB, `maxOldGenerationSizeMb` 512 (`packages/code-runtime/code-runtime-worker-thread/src/index.ts:240-243`).

**Two independent time budgets.** `computeMs` meters worker *busy time* via `eventLoopUtilization()`, so a slow awaited tool does not consume it but a hot loop does; `maxWallMs` bounds total elapsed time including unresolved waits (`index.ts:539-545`; `2026-06-15-code-mode.md:79`). This split is load-bearing and non-obvious: a single timeout cannot both tolerate a legitimately slow tool call and kill a spin loop.

**Nested calls re-enter the normal pipeline.** Each binding invocation dispatches through the full tool pipeline with a deterministic call id and the outer execution token as `parent` (`packages/core/tools/src/code-mode.ts:477`; `2026-06-15-code-mode.md:45`). Sub-calls overlap up to `maxParallelSubCalls`, default 10, and only for calls the tool itself classifies concurrency-safe; an exclusive call drains the pool and runs alone (`packages/core/tools/src/index.ts:776`; `2026-06-15-code-mode.md:51`). Because injecting sub-results inside `run_code` would break parent call/result adjacency, per-call contexts are deferred through the parent and appended after the outer result (`:49`).

Two things the note is explicit about, and that any Autoto version would inherit:

**The worker is containment, not a security boundary.** "Model code can reach Node APIs and has authority comparable to the bash tool" (`:84`). The justification is comparative: the harness already ships a bash tool with strictly *more* ambient authority, so Code Mode adds an empty environment, a heap cap, a separate isolate, and hard termination on top of an unchanged trust posture (`:23`). `node:vm` was rejected as the reference runtime precisely because it is *not* isolation and cannot interrupt a hot loop (`:107`).

**The failure taxonomy is orthogonal.** `exception | timeout | abort | worker-exit | invalid-output | output-limit` — a timed-out run is not an exception, an abort is not a timeout, a lossy completion is not an overflow, and a substrate exit is none of them (`:66`). This is the part of the design most likely to be under-built in a reimplementation and most likely to cause bad model behavior when it is, because the model self-corrects off the failure kind.

Also worth noting for Autoto's purposes: the note itself declines to claim unconditional token savings. The SDK declaration prefix "can rival the native schemas it complements," `'both'` mode carries two representations, and measured guidance on when to prefer which mode is explicitly deferred to post-ship learning (`:127`). And they rejected the Cloudflare-faithful always-on form for a coding agent, because "a coding agent's bread-and-butter single calls (`bash`, `read`, `edit`) are already ideal as native calls, and forcing every edit through a program taxes the common case" (`:113`).

## 4. What Autoto has today

This section is existing behavior, and it is the baseline Code Mode would have to beat.

### 4.1 The tool contract

```go
type Tool interface {
    Name() string
    Description() string
    Schema() any
    Risk(input json.RawMessage) Risk
    Execute(ctx context.Context, call Call, env Env) (Result, error)
}
```

(`internal/tools/tool.go:198-204`.) Risk is a four-value ladder — `read`, `write`, `exec`, `danger` (`internal/tools/tool.go:10-17`) — and it is computed per call from the input, not per tool, which is why `Bash` can be read-ish for `ls` and dangerous for `rm -rf` (`internal/tools/bash.go:48`).

Schemas are generated by reflection over the Go input struct, with `desc` and `jsonschema` struct tags supplying descriptions and constraints (`internal/agent/tool_schema.go:12-25`, `:120-171`, `:187-227`). Object schemas are pinned closed-world unless the author opts out (`internal/agent/tool_schema.go:29-58`). This is good news for one narrow part of the feature: Autoto could generate an SDK declaration from the same source the wire schema comes from, and the closed-world default means the generated types would be accurate rather than permissive.

26 core tools are registered (`internal/tools/registry.go:60-87`), and the catalog is extensible at runtime through `ToolSource`/`Resolver` for plugins and MCP (`internal/tools/tool.go:164-173`), so any generated SDK surface is unbounded in size, not fixed at 26.

### 4.2 The approval gate

`executeToolForLoop` (`internal/agent/runner_tools.go:282-433`) is the single funnel. In order: normalize input against the schema, classify risk, apply plan/conversation mode denial, hard-block `RiskDanger`, resolve policy permission, run danger reflection, and only then either execute or wait for a human.

Two details matter disproportionately for this assessment:

- **`RiskDanger` is refused in every mode.** It is not an approval prompt; it is a block with a warning (`internal/agent/runner_tools.go:336-345`).
- **Danger reflection is a model call.** Before a `write`- or `exec`-risk call that static policy would otherwise run silently, `reflectBeforeExecution` asks a model to review the concrete action and may downgrade the decision to ask or deny (`internal/agent/runner_tools.go:349`; `internal/agent/danger_reflection.go:177-210`). It fires only for `Bash` at exec risk and for unconfined write tools (`internal/agent/danger_reflection.go:414-424`), it is skipped for human- and session-approved calls, and results are cached by fingerprint (`danger_reflection.go:204-210`).

Human approval waits on a 10-minute timer (`internal/agent/runner.go:140`, used at `internal/agent/runner_tools.go:693` and `:705`). Approvals are invalidated if permission or policy generation moved underneath them (`internal/agent/runner_tools.go:407-419`).

There is **no OS-level sandbox anywhere in Autoto.** Bash runs `cmd /C` on Windows and `/bin/sh -c` elsewhere, as the executing user (`internal/tools/bash.go:248-256`). The Windows Job Object in `internal/process/group_windows.go:139-151` sets only `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` — it is process-tree reaping, not confinement; no memory limit, no UI restriction, no token restriction. What stands between the model and the machine is the approval gate plus `internal/tools/command_facts.go`, which parses the command with the grammar of the shell that will actually run it and refuses to treat an unparseable command as safe (`internal/tools/command_facts.go:23-49`, `:56-60`).

### 4.3 Where results become messages

`internal/agent/continuation.go:657-726` is the per-call loop. Each result goes through `processToolResultForModel` (`continuation.go:671`), becomes a `tool_result` content block and a persisted user-role message (`:672-675`), and is published. The tool execution group ledger records a terminal status and a bounded output summary per item (`:698-710`).

`processToolResultForModel` is the pipeline hook (`internal/agent/tool_output_pipeline.go:14-19`). That single indirection is the reason the tool output pipeline could be added without touching the tool interface, and it is the natural place any "curate what re-enters context" mechanism would attach.

### 4.4 What already keeps intermediate results out of context

**The tool output pipeline is implemented, not proposed.** `docs/TOOL_OUTPUT_PIPELINE_PROPOSAL.md` is the design note; the code is `internal/toolpipeline/manager.go` wired through `tools.ToolOutputPipelineService` (`internal/tools/tool.go:121-128`) and the `StartPipeline`/`EndPipeline` tools (`internal/tools/pipeline.go`). While a pipeline is active, each eligible tool result is captured whole and the model receives only an alias, byte count, error flag, and a preview defaulting to 100 characters (`internal/toolpipeline/manager.go:266-285`, limits at `:13-24`). `EndPipeline` then reduces the captures through a restricted in-process rule — "`from`, `cat`, `grep`, `head`, `tail`, `sort`, `uniq`, and `cut`; no shell command is executed" (`internal/tools/pipeline.go:53`) — and only that reduction enters context, default 12 KB and capped at 50 KB (`internal/toolpipeline/manager.go:19-20`, `:187-193`).

So Autoto already has a curated-output mechanism *and* a small non-Turing-complete post-processing language over captured results. Code Mode's context-reduction half is not a new capability for Autoto; it is a more expressive version of a capability that shipped.

**Read-risk calls in one turn already overlap.** `prefetchToolCallResults` runs eligible calls ahead of the serial loop on a 4-worker pool (`internal/agent/tool_prefetch.go:51-105`, `:27`). Eligibility is deliberately narrow: read risk for that input, resolved to a plain allow by policy, not a pipeline control tool, and not one of `Bash`/`Agent`/`Task`/`MCPCallTool`/`Symbols` (`internal/agent/tool_prefetch.go:37-43`, `:64-76`). Fewer than two eligible calls and it returns nil.

**Subagents already move a whole work stream out of the parent context.** `AgentTool` submits a durable background task and returns a handle immediately (`internal/tools/agent_task.go:162-185`), the child runs its own loop with its own context, and the parent is woken with a report bounded at `maxAgentResultBytes = 4096` (`internal/background/agent.go:26`). Child depth is capped and per-agent concurrency limited (`internal/background/agent.go:119-124`).

**Compaction handles the residue.** Standard windows prune at 80% and compact at 90%; large windows (over 600k tokens, `internal/agent/context_management.go:16`) at 85% and 92% (`internal/config/defaults.go:353-359`, asserted at `internal/config/defaults_test.go:118`). There is manual, background, and budget-driven compaction, plus dedicated tool-result compaction (`internal/agent/runner_context.go:748`, `:797`, `:854`).

### 4.5 No scripting engine, no sandbox

`go.mod:5-22` lists no JS engine, no WASM runtime, no Lua, no Starlark; a search of `go.sum` for `dop251/goja`, `traefik/yaegi`, `tetratelabs/wazero`, `yuin/gopher-lua`, and `go.starlark.net` returns nothing. The one thing that looks like an interpreter is not one: `mvdan.cc/sh/v3` is present but only `mvdan.cc/sh/v3/syntax` is imported, and only in `internal/tools/command_facts.go:10` — it is a shell *parser* used for static risk classification. Autoto executes shell commands by handing them to the platform shell, never by interpreting them.

This means Code Mode would introduce Autoto's first embedded execution engine and its first in-process untrusted-code boundary. That is not a reason on its own to refuse, but it does mean there is no existing infrastructure to amortize the cost against.

## 5. Question 2: what would run the program

Assessed for four properties: isolation reality, whether the program can express `await`-style tool calls, dependency weight, and Windows behavior. **Autoto runs on Windows** (`docs/WINDOWS_RUN.md`, and `internal/tools/bash.go:250-252` special-cases it), so Windows is a first-class constraint and not a portability footnote.

### goja (embedded ECMAScript, pure Go)

*Isolation:* Better than it gets credit for. goja exposes no host APIs unless the host injects them — no filesystem, no network, no process spawn — so a bare goja runtime has a **smaller** default attack surface than a Node worker thread, which reaches all of Node. It also supports interrupting a running script from another goroutine, which covers the hot-loop case that killed `node:vm` for the reference implementation (`2026-06-15-code-mode.md:107`). What it does not give you is a memory cap: there is no heap limit equivalent to `maxOldGenerationSizeMb`, so an allocation loop in model-written code threatens the Autoto process itself. That is a materially worse failure mode than a dead worker thread.

*`await`:* **This is the problem.** goja implements Promises, but it has no event loop; draining the job queue is the host's responsibility, and a goja runtime is not safe for concurrent use from multiple goroutines. Expressing `await tools.Read(...)` therefore means writing the scheduler that the reference implementation gets from Node for free: the program runs on one goroutine, tool dispatch happens on others, and every resolution must be marshalled back onto the interpreter goroutine before jobs are drained. It also means Autoto owns the `computeMs`/`maxWallMs` distinction manually, since there is no `eventLoopUtilization()` to meter against.

*Weight:* One pure-Go dependency, no cgo, cross-compiles cleanly. Lightest of the real options.

*Windows:* Fine. Pure Go, no platform behavior.

*Uncertainty flagged:* I have not prototyped the promise-resolution bridge. It is the single largest unknown in this section, and the estimate in §8 assumes it works but costs a bounded amount of custom scheduling code. If it turns out to need more than that, the §8 numbers are low.

### yaegi (embedded Go interpreter)

*Isolation:* Weak and awkward to bound. The host controls which packages are exported to the interpreter, so a restricted symbol set is possible in principle, but the interpreter shares the host process and there is no memory or CPU bound. Interrupting a running interpreted loop is not a supported operation the way goja's interrupt is.

*`await`:* Expressible, and arguably more naturally than in JS — goroutines and channels are the native idiom, and a `tools.Read(...)` binding is just a blocking Go call. No scheduler to build.

*Weight:* Heavy. yaegi carries extracted stdlib symbol tables per Go release, and that extraction historically lags new Go versions. Autoto is on `go 1.26` (`go.mod:3`), which puts it at the front of that lag. *Flagged as uncertain:* I have not verified yaegi's current Go 1.26 support, and this consideration would need checking before it counts as a real objection rather than a suspected one.

*Windows:* Fine.

*The real objection is the model.* Models write far more idiomatic TypeScript and Python than Go, and the reference implementation's entire premise is that "LLMs are better at writing code than at emitting tool calls, because they have seen millions of lines of real code" (`2026-06-15-code-mode.md:13`). Choosing the language the model is weakest at discards the premise. Go's error handling also makes a five-call program noticeably longer than its TypeScript equivalent, which eats into the token savings the feature exists to produce.

### gopher-lua (embedded Lua 5.1, pure Go)

*Isolation:* Comparable to goja — nothing exposed unless injected — plus context-based cancellation and configurable registry and call-stack sizes. Still no true memory cap.

*`await`:* Coroutines exist, but Lua has no async idiom a model will reach for unprompted. Tool calls would be plain blocking calls, which is fine semantically but gives up in-program parallelism unless the host schedules coroutines itself.

*Weight:* Light, pure Go.

*Windows:* Fine.

*Objection:* Weakest model fluency of the four, and 1-based indexing plus `nil`-vs-`false` semantics are a reliable source of model errors. Cheapest to embed, most expensive in model correctness.

### wazero (WASM, pure Go)

*Isolation:* The only genuine option. Linear memory is bounded and inaccessible to the host's address space, there is no ambient capability whatsoever, and modules can be closed from outside. This is the only candidate that could honestly be described as a security boundary rather than containment, and therefore the only one that would let Autoto run programs at a *lower* trust level than Bash rather than an equal one.

*`await`:* Nothing for free. Host calls are synchronous imports; async is whatever you build on top.

*Weight:* Deceptively large. WASM runs compiled modules, and there is no way to compile the model's source at runtime. Making this work means embedding a language interpreter already compiled to WASM — QuickJS for JavaScript, or CPython, both multi-megabyte artifacts — and then writing the host-call bridge between that interpreter's foreign-function interface and Autoto's tool dispatch. That is two hard subsystems, not one.

*Windows:* Fine, and wazero's pure-Go compiler avoids cgo.

*Assessment:* Right answer for a hosted multi-tenant product. Wrong answer for a local single-user desktop agent whose existing Bash tool already runs arbitrary commands as the user. It would be the most-isolated component in a system with no other isolation — real engineering spent on a boundary that the adjacent Bash tool renders moot.

### Shelling out to Node or Python

*Isolation:* None. A child process with the user's full authority, which is exactly Bash.

*`await`:* Free and correct — this is what the reference implementation does.

*Weight:* No Go dependency, but a hard runtime prerequisite Autoto cannot satisfy itself.

*Windows:* **This is where it fails.** Neither Node nor Python is guaranteed present on Windows. Worse, `python.exe` on a default Windows 10/11 install is frequently the Microsoft Store stub, which is not an interpreter and opens the Store instead — a detection routine that checks for the binary on `PATH` gets a false positive and the feature fails at first use, not at startup. Autoto is a desktop application (`docs/DESKTOP_PACKAGING.md`) and cannot make its tool catalog depend on a toolchain a given user may not have. A feature that silently degrades on the primary platform is worse than one that does not exist.

*The disqualifying objection is not availability, it is classification.* Autoto's exec gate depends on parsing the command that will run (`internal/tools/command_facts.go:56-60`) and on refusing to call an unparseable command safe (`CommandFacts.Unclassified()`, `internal/tools/command_facts.go:41-49`). `node /tmp/program-abc123.js` parses to a program name and an opaque path. The static classifier learns nothing, and Autoto would have introduced its first exec path that is unclassifiable by construction. That is not bash-equivalent trust; it is bash *minus* the analysis layer that makes Autoto's Bash tool defensible.

### Runtime recommendation

**If the feature were built: goja, with a host-driven job queue and read-risk-only bindings.** It is the only option that is simultaneously pure Go, Windows-clean, credible on model fluency, and interruptible. The heap-cap gap is real and would have to be documented as an accepted risk rather than solved, since Go offers no per-goroutine memory bound.

But note what happens to the trust argument under that choice. The reference implementation justifies its posture by comparison: worker threads are acceptable *because* the harness already ships a bash tool with more authority (`2026-06-15-code-mode.md:23`). Autoto can make the same comparison and it holds — but only if the program is confined to bindings that are themselves gated. A goja program with a `Bash` binding is Bash with the static classifier removed. A goja program with only read-risk bindings is strictly weaker than Bash and needs no new trust argument at all. That observation is what makes §7's answer the only workable one.

## 6. Question 1: what would Autoto actually gain

Setting aside cost, here is the honest ledger against §4.4's baseline.

**Round-trips.** Real but bounded. Consider the canonical workload: grep for a symbol, read every file that matched, report which ones need changing. Today that is one turn for `Grep`, then one turn issuing *N* parallel `Read` calls (overlapped 4-wide by `internal/agent/tool_prefetch.go:27`), then one turn to conclude — **3 model turns**. Under Code Mode it is one `run_code` turn plus one concluding turn — **2 turns**. Saving one turn out of three is worth something; it is not the order-of-magnitude change the framing implies, and it exists only because Autoto already parallelizes within a turn.

The saving grows when the *number of turns* depends on data the model does not have upfront: walk a dependency chain until a condition holds, retry with a widened pattern when the first search returns nothing, page through results until a match appears. Today each iteration is a turn. Under Code Mode it is a `while` loop. **This is the one place PTC does something the existing machinery cannot.**

**Tokens.** Genuinely ambiguous, and the reference implementation says so — the SDK declaration "can rival the native schemas it complements" (`2026-06-15-code-mode.md:127`), and Autoto's SDK would have to describe all 26 core tools (`internal/tools/registry.go:60-87`) plus every dynamic plugin and MCP tool (`internal/tools/tool.go:164-173`). Against that fixed per-request cost, the variable saving is intermediate tool output — which the pipeline already suppresses to a 100-character preview when the model asks it to (`internal/toolpipeline/manager.go:279-280`). The marginal token saving over *pipeline-plus-prefetch* is small and could plausibly be negative for sessions that do not use programs, since the SDK prefix is paid every request while the pipeline is paid only when used.

**Context pressure.** Nothing meaningful. Intermediate results already do not accumulate: the pipeline suppresses them on demand, compaction removes them at 80/90% (`internal/config/defaults.go:353-359`), and subagents never put them in the parent at all (`internal/background/agent.go:26`).

**Expressiveness the alternatives cannot reach.** A subagent can loop and branch — it is a whole agent — but each of its iterations costs a model turn *in the child*, and its report is capped at 4096 bytes (`internal/background/agent.go:26`). So the comparison is not "PTC versus nothing," it is "one deterministic program loop versus one child agent's model loop." The program is cheaper and exact where the work is mechanical; the child is better where the work needs judgment. That is a real distinction, and it is the strongest honest argument *for* the feature.

**Net.** PTC adds deterministic, data-dependent control flow over tool calls, and saves roughly one model turn on multi-step exploration. It does not meaningfully add context savings, and its token effect is unproven in the reference implementation's own assessment. That is a modest gain, and §8 prices it.

## 7. Question 3: the approval problem

This is the crux. Autoto's security model is a per-call gate that classifies a *concrete* action: `risk := tool.Risk(call.Input)` at `internal/agent/runner_tools.go:317`, then policy, then reflection, then possibly a human (`internal/agent/runner_tools.go:346-352`, `:389`). Every layer is built on knowing what the specific call does. A program that makes 50 calls in a loop presents nothing to classify until it is already running.

### What the reference implementation does

Nothing special, deliberately. `run_code` traverses the complete tool pipeline like any other tool, so "a permission plugin can inspect the program text before it runs" (`2026-06-15-code-mode.md:43`), and each nested call independently traverses the full pipeline with the outer token as `parent` (`:45`; `packages/core/tools/src/code-mode.ts:477`). The gate is per nested call, exactly as in native mode, and the design leans on the harness's existing policy layer plus the argument that bash already has more authority (`:84`).

That answer does not transfer, for two reasons specific to Autoto.

First, **Autoto's exec gate is a static classifier, not a policy plugin.** The thing that makes Autoto's Bash tool defensible is `AnalyzeBashCommand` parsing a real command line (`internal/tools/command_facts.go:56-60`) and refusing to bless what it cannot parse (`:41-49`). There is no equivalent for a program, so "trust posture is bash-equivalent" is false for Autoto — a program is bash *without* the analysis.

Second, **Autoto's gate contains a model call.** `reflectBeforeExecution` reviews write- and exec-risk actions before they run (`internal/agent/danger_reflection.go:177-210`, applicability at `:414-424`). Inside a 50-call program loop, that is up to 50 reflection model calls, minus fingerprint-cache hits (`danger_reflection.go:204-210`). A feature whose entire purpose is removing model round-trips would reintroduce them one per mutating call.

### The options, honestly

**(a) Approve the program text once, up front.** Cheapest to build: `run_code` is a tool, so `executeToolForLoop` already gates it, and a `RiskExec` classification would route it to a human with the program as the command text (`internal/agent/runner_tools.go:389`).

The trade-off is that the human approves *text*, not effects. Reading 30 lines of model-written code and correctly predicting its filesystem writes is a strictly harder ask than reading `rm -rf build/` — and Autoto's own design says it does not trust humans to do the harder version of this, which is why `RiskDanger` is a hard block rather than a prompt (`internal/agent/runner_tools.go:336-345`). It also creates an obvious laundering path: any call that would have been individually refused becomes reachable by embedding it in an approved program. This option is only defensible if the program's bindings are all things the user would have approved anyway — which is option (c).

**(b) Intercept each nested call and pause.** Most faithful to Autoto's model, and it is what the reference implementation does. Nested calls would re-enter `executeToolForLoop` with a parent token, inheriting risk classification, plan-mode denial, danger blocks, reflection, and human approval unchanged.

The trade-off is that it destroys the feature's purpose. Fifty calls means up to fifty approval prompts, each parking the run on a 10-minute timer (`internal/agent/runner.go:140`) with the program suspended mid-execution — and the program must hold its goja goroutine open across every one of those waits. Any approval that arrives after a permission-generation change is invalidated (`internal/agent/runner_tools.go:407-419`), so a long program can have its 40th call refused on a technicality after 39 succeeded, with no way to unwind the first 39. Worst case a program becomes *slower and more interruptive* than the native calls it replaced.

There is also a subtler failure: in a subagent the prompt is addressed to an agent nobody is watching, which is why unattended child calls are denied immediately rather than waited on (`internal/agent/runner_tools.go:374-388`). A program running inside a subagent would therefore have its first mutating call denied, mid-loop, always.

**(c) Restrict programs to read-risk tools only.** The program's bindings are limited to tools reporting `RiskRead` for the given input. Anything else is not bound at all — not denied at call time, but absent from the generated SDK, so the model cannot write the call in the first place.

This is the option that fits, and the argument is that Autoto already made this exact decision three times:

- The tool output pipeline — the closest existing analog to Code Mode — is available *only* in `readOnly` permission mode or plan execution mode (`internal/agent/policy.go:71-73`, enforced at `:91-93`). Autoto already decided that curated-output machinery belongs in read-only contexts.
- Speculative parallel execution is restricted to read-risk calls that policy already resolves to a plain allow (`internal/agent/tool_prefetch.go:37-43`, `:55-59`), explicitly because read risk is "the one risk tier with no side effect for permission resolution or the workspace to race against" (`internal/agent/tool_prefetch.go:107-111`).
- Plan mode ships a named read-only allowlist of exactly the tools worth exploring with (`internal/agent/policy.go:23-35`).

The trade-offs are real and worth stating. Read-only programs cannot do the mutating work — no bulk rename across 40 files in one program, which is a genuinely attractive use case. And "read risk" is not the same as "harmless": `WebFetch` is read-risk (`internal/tools/webfetch.go:43`) and a loop over attacker-influenced URLs is an exfiltration channel that a per-call gate would at least have surfaced one prompt at a time. That needs a per-run call budget and a bound on total sub-calls regardless.

But the compensating properties are decisive. Read-risk calls are already auto-approved in most modes, so there is nothing to prompt for. `reflectableToolCall` returns false for read risk (`internal/agent/danger_reflection.go:414-424`), so the per-call model round-trip problem disappears entirely. And the resulting program is strictly weaker than the Bash tool Autoto already ships, which means the trust posture needs no new argument — the comparative justification the reference implementation uses (`2026-06-15-code-mode.md:23`) actually holds, rather than being asserted while missing the classifier that backs it.

**(d) Sub-call budget with escalation on first mutation.** Programs get read bindings plus a bounded number of write bindings; the first write-risk call suspends the program and asks. This is (b) with a lower prompt count, and it inherits (b)'s worst property — a suspended program holding an interpreter goroutine across a 10-minute human wait — while adding a new one: partial-effect programs that fail halfway with no rollback story. Not recommended.

### Resolution

**Option (c), unconditionally, and (a) only as the mechanism for approving the `run_code` call itself in modes where read tools are not auto-allowed.** Concretely, were this built: bindings generated only for tools returning `RiskRead` for the call's input; `run_code` absent from the catalog entirely in plan and conversation modes (matching `internal/agent/policy.go:106-116`); a per-run sub-call ceiling; nested calls still routed through `executeToolForLoop` so risk is re-checked at dispatch rather than trusted from generation time; and — because a tool's risk depends on its input (`internal/tools/tool.go:202`) — a runtime refusal if a bound tool returns anything other than `RiskRead` for the arguments the program actually passed. The SDK must not be treated as an authorization decision; it is a discoverability filter, and the gate stays at dispatch.

Options (a) alone and (b) alone are both unacceptable: (a) because it launders the gate Autoto's design deliberately refuses to let humans override, (b) because it converts the feature into a slower, more interruptive version of what it replaced.

## 8. Question 4: the prerequisite problem

### Autoto's tool results are prose

```go
type Result struct {
    Output  string         `json:"output"`
    IsError bool           `json:"isError,omitempty"`
    Meta    map[string]any `json:"meta,omitempty"`
}
```

(`internal/tools/tool.go:91-95`.) `Output` is a rendered human-readable string. `Meta` is telemetry and presentation, not payload — surveying every `Meta:` literal in `internal/tools/`, it carries counts, truncation flags, paths, status codes, and background task ids: `{"count", "files", "outputMode", "truncated"}` for Grep (`internal/tools/grep.go:168-173`), `{"count"}` for Glob (`internal/tools/glob.go:187`), `{"path", "truncated", "binary"}` for Read (`internal/tools/read.go:105`), `{"truncated"}` for Bash (`internal/tools/bash.go:299`).

The clearest demonstration is Grep. It builds `[]grepFileResult` with per-file match structures, renders them to a string, and returns the string — the structure is discarded and only aggregate counts survive in `Meta` (`internal/tools/grep.go:156-173`). A program calling a projected `Grep` would receive text like `path:12: matched line` and would have to re-parse it, including correctly ignoring the `\n...[truncated]` marker appended at `:166`. The same holds for `LS` (`internal/tools/ls.go:165`) and `Symbols` (`internal/tools/lsp.go:1021`, `:1078`, `:1149`).

There is one telling exception. `MCPCallTool` smuggles the real payload through telemetry: `Meta: {"toolName": ..., "raw": string(result.Raw)}` (`internal/tools/mcp.go:157`) — a JSON document stringified into a metadata map because there is no field for it. `MCPListTools` does the same (`internal/tools/mcp.go:86`). That is the missing seam showing through: a tool that already *had* a structured result had nowhere to put it.

So yes, this is exactly the prerequisite problem, and it is worse than it looks. Under Code Mode the generated SDK could describe every tool's *arguments* precisely — the reflection-based schema generator at `internal/agent/tool_schema.go:120-171` is good — while every return type would be `Promise<string>`. The model would be handed a typed API whose outputs are all untyped text, and every program would open with a hand-rolled parser for prose that no contract stops a tool from reformatting.

### The reference implementation hit this and paid for it separately

This is not speculation. DeepSeek Harness shipped Code Mode with string returns and then wrote a second design note to undo it: "Code Mode originally projected each nested tool result back from `ContentBlock[]` into one string. That preserved the human-readable Native presentation but erased the canonical result the tool had already produced: programs had to scrape job ids and dynamic mount ids from prose, structured search and workflow results lost their shape... The generated SDK could describe arguments but could only promise `Promise<string>` regardless of the tool's real output" (`.agents/notes/implemented/feature/2026-07-20-code-mode-typed-tool-returns.md:9`).

The fix was a mandatory contract change across every tool: each declares an output schema plus a pure renderer, `defineTool` infers both projectors, and "**registration rejects a missing declaration... there is no content-return compatibility path**" (`.agents/notes/implemented/architecture/2026-07-20-canonical-tool-output-contract.md:15-25`). The result was a per-tool DTO table covering roughly two dozen tool families — `read` became `{ path, offset, lines: [{ number, text }], totalLines }`, `grep` became `{ matches: [{ path, lineNumber, line }] }`, and so on (`:41-61`). They explicitly rejected the alternative of returning rendered text to programs, "because callers would continue scraping prose for job ids, mount ids, paths, and structured provider results" (`:69`), and rejected letting tools return both value and content, "because two author-owned results can disagree and policy cannot state which one is authoritative" (`:71`).

Autoto is currently in the state DeepSeek Harness was in *before* that refactor. Building Code Mode without it means knowingly reproducing a mistake whose correction is documented.

### Is the refactor worth doing on its own?

**Yes, and this is the most useful conclusion in this document.** A structured value alongside `Output` pays off in five places that have nothing to do with Code Mode:

1. **MCP stops lying about `Meta`.** `internal/tools/mcp.go:157` and `:86` get a real home for `result.Raw` instead of a stringified JSON document in a telemetry map.
2. **The tool output pipeline gets sharper.** Its rule vocabulary today is line-oriented — `grep`, `head`, `tail`, `sort`, `uniq`, `cut` (`internal/tools/pipeline.go:53`) — because lines are all it has. Given structured captures it could select fields, which is both more precise and cheaper than `cut`-ing rendered columns.
3. **UI result cards stop re-deriving from prose.** `toolFinishedEventDataWithResolution` ships the result into events (`internal/agent/runner_tools.go:653`) and the frontend renders from it; structured values let cards render fields rather than reformatting text.
4. **Lifecycle hooks get a real contract.** `dispatchToolLifecycle` already hands `*tools.Result` to `EventToolAfter` (`internal/agent/runner_tools.go:654`); today a hook that wants the grep match count must parse `Output` or trust `Meta`.
5. **Persistence improves for free.** `json.Marshal(result)` already stores the whole struct (`internal/agent/runner_tools.go:649`), so an added field is persisted and API-visible with no schema migration.

Crucially, Autoto can do this **additively**, which the reference implementation could not. Their `ContentBlock[]` return *was* the model-facing payload, so replacing it required a hard cutover with no compatibility path (`2026-07-20-canonical-tool-output-contract.md:25`). Autoto's `Output` string stays exactly as it is and remains what reaches the model; a new field carries the structure. Tools opt in one at a time, nothing breaks while the migration is partial, and the model-facing wire format is byte-identical throughout. That is a substantially cheaper migration than the one being cited as evidence for its necessity.

**Recommendation: do this, whether or not Code Mode is ever built.** It is the one piece of the feature with standalone value.

## 9. Question 5: cost and staging

### Cost anchor from the reference implementation

The shipped TypeScript-only path, counted in the clone:

| Component | Production | Tests |
|---|---|---|
| `code-runtime` seam (`src/`) | 269 | — |
| `code-runtime-worker-thread` (`src/`) | 1,560 | 1,647 |
| Registry side: `code-mode.ts` + `ts-types.ts` | 910 | 1,821 |
| **Total** | **~2,740** | **~3,470** |

About 6,200 lines for the TypeScript path alone (`py-types.ts`, a further 782 lines, is excluded). For scale: Autoto's entire non-test `internal/tools/` package is 8,419 lines across 33 files. So the reference implementation of this one feature is roughly three-quarters the size of Autoto's whole tool layer.

And that is with Node supplying the hard parts. `worker_threads` for the isolate, `stripTypeScriptTypes` for type erasure, structured clone for the value boundary, `resourceLimits` for the heap cap, and `eventLoopUtilization()` for busy-time metering are all platform primitives there (`2026-06-15-code-mode.md:75-79`). **In Go, every one of those is either hand-built or absent.** A Go port is not a translation of 2,740 lines; it is those lines plus the substrate underneath them.

### Estimate

Engineering estimates, one developer, including tests to the standard `docs/ARCHITECTURE.md:212-222` requires. Ranges are wide because the goja scheduling bridge is unprototyped (§5).

| Stage | Scope | Estimate |
|---|---|---|
| **0a** | Structured-result seam: add the field to `tools.Result`, define the value contract, convert the six highest-value tools (`Read`, `Grep`, `Glob`, `LS`, `Bash`, `Task`) plus MCP's misplaced `raw` | 1–1.5 weeks |
| **0b** | Convert the remaining core tools; pipeline field-selection over structured captures; UI cards read fields | 2–3 weeks |
| **1a** | goja embedding: interpreter lifecycle, interrupt, promise-resolution bridge and job queue, dual time budgets metered by hand | 2–4 weeks |
| **1b** | SDK generation from existing tool schemas, reusing `internal/agent/tool_schema.go` | 1 week |
| **1c** | `run_code` tool, read-risk binding projection, nested dispatch through `executeToolForLoop` with a parent token, sub-call ceiling, re-check of risk at dispatch | 2–3 weeks |
| **1d** | Failure taxonomy (all six kinds, distinctly), output ledger, event/persistence surface for sub-dispatches, UI presentation for a program call | 1.5–2 weeks |
| **1e** | Prompt/SDK cost measurement and mode gating; the reference implementation defers this to post-ship learning (`2026-06-15-code-mode.md:127`) and Autoto would inherit that unknown | 1 week + ongoing |
| | **Stage 1 total** | **7.5–11 weeks** |

Plus a permanent maintenance surface: every new tool needs an SDK projection, the goja dependency needs tracking, and the model-facing program contract becomes a compatibility commitment.

Set that against §6's honest gain — one model turn saved on multi-step exploration, plus data-dependent control flow, with no meaningful context saving and an unproven token effect.

### Staging

**Stage 0 (recommended, ~3–4.5 weeks): structured tool results.** Justified in §8 on its own merits. Do this first regardless of the Code Mode decision, because it is the prerequisite *and* independently valuable, and because doing it reveals how much of the perceived gap was really a result-shape problem.

**Stage 0.5 (recommended, ~1–1.5 weeks): close the actual gap cheaply.** After Stage 0, two small additions cover most of what §6 identified:

- **A batch read.** `Read` accepting multiple paths turns "grep, then read each of 12 hits" from 2 turns into 2 turns with one call instead of 12 — and more importantly makes the common fan-out pattern expressible without a program at all. Roughly one tool, bounded by the existing per-read caps (`maxReadBytes = 100000`, `internal/tools/read.go:22`).
- **Field selection in `EndPipeline`.** Extend the existing rule vocabulary (`internal/tools/pipeline.go:53`) with a structured selector over Stage 0's values. The pipeline already captures, aliases, and reduces; it currently reduces *lines* because lines are all it has.

Neither needs a runtime, a sandbox, or an approval-model change, and both land inside machinery that already exists.

**Stage 1 (not recommended, 7.5–11 weeks): Code Mode proper.** If it is ever built, build it in the §7(c) shape — read-risk bindings only — and treat §5's goja choice as provisional until the promise bridge is prototyped.

**A partial version worth shipping first?** Not of Code Mode itself. The natural candidate — `run_code` with read-only bindings and no nested-dispatch integration — still needs the runtime, the SDK generation, the failure taxonomy, and the budget metering, which is most of Stage 1's cost. There is no cheap slice of the runtime. The genuinely cheap slice of the *benefit* is Stage 0.5, which is why it is separated out.

## 10. Recommendation

**Against Code Mode. For the structured-result seam it would have depended on.**

The case in one paragraph: Autoto has already solved, by other means, the two problems Code Mode is usually sold on. Intermediate results already stay out of context — the tool output pipeline curates them to a 100-character preview on demand (`internal/toolpipeline/manager.go:266-285`), subagents never surface them at all (`internal/background/agent.go:26`), and compaction removes what leaks (`internal/config/defaults.go:353-359`). Independent reads already overlap within a turn (`internal/agent/tool_prefetch.go:27`). What remains is data-dependent control flow, which is real but worth roughly one saved model turn on exploration workloads — and buying it costs 7.5–11 weeks, Autoto's first embedded execution engine, and a permanent model-facing program contract, on a platform where the cheapest runtime option is also the one that would create Autoto's first unclassifiable exec path.

The approval problem is resolvable but only by narrowing the feature until much of its appeal is gone. Read-risk-only bindings are the sole option that neither launders Autoto's gate (§7a) nor reintroduces the per-call round-trips the feature exists to remove (§7b). Read-only programs cannot do bulk edits, which is the use case people actually want. That is not a flaw in the analysis; it is the honest shape of the feature under Autoto's security model.

The prerequisite is real, and it is also the opportunity. Prose-only tool results would undercut PTC exactly as they did in the reference implementation, which had to correct it in a separate mandatory refactor (`2026-07-20-code-mode-typed-tool-returns.md:9`). But Autoto can add structured values *additively* where they could not, and the seam pays off in MCP, the pipeline, the UI, hooks, and persistence regardless. **Do Stage 0 and Stage 0.5. Revisit Stage 1 only if the conditions below change.**

Autoto also already has an escape hatch for genuinely program-shaped work: the Bash tool. A model that needs a loop can write a shell loop today, and it goes through static command analysis (`internal/tools/command_facts.go:56-60`) and the approval gate. Code Mode's marginal value over Bash-plus-pipeline is narrower than it appears, and unlike Code Mode, Bash arrives with a classifier attached.

## 11. What would change this verdict

Stated so this can be revisited on evidence rather than re-argued:

1. **Measured turn counts.** If instrumentation showed a substantial share of runs spending many turns in read-only exploration loops that Stage 0.5 does not flatten, the §6 gain would be larger than one turn and the calculus shifts. This is the strongest possible counter-argument and it is currently unmeasured — flagged as the main gap in this assessment.
2. **A real sandbox for other reasons.** If Autoto grows an OS-level sandbox for Bash (a container backend, a Windows AppContainer), the isolation cost of running programs largely disappears and mutating programs become discussable. §5's `wazero` objection is that it would be the only boundary in a system with none; that objection dies if there are others.
3. **Provider-side PTC becoming portable.** If programmatic tool calling stops being a single-vendor beta and becomes something the model providers offer uniformly, the entire runtime section (§5) — the most expensive and least certain part — is deleted from the problem, and only the structured-result prerequisite and the approval question remain.
4. **Stage 0 changing the picture.** Structured results might make the pipeline expressive enough that the remaining gap closes on its own — or might reveal that programs are the natural consumer of the new values. Either outcome is informative, which is another reason to do Stage 0 first.

Point 1 is worth acting on now: it is cheap to measure and it is the number this whole assessment turns on.
