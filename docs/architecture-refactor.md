# Refactor: swappable frontend / agent / provider / connector

Goal: every element swappable and testable on its own.

```
frontend  (tui | console | minimal | headless | rpc)
    │  Submit(prompt), Interrupt();  receives events through Sink
    ▼
agent     (loop, tools, session, retry, fallback)
    │  sees ONLY: ModelClient + Sink
    ▼
provider  (universal: model catalog, URI, auth, connector selection)
    ▼
connector  openai │ anthropic │ gemini │ responses ║ fake │ replay │ flaky
    ▼
HTTPDoer  (injected per connector)
```

Key point: the connector is swappable, not the provider — there is a single
provider implementation, explicitly constructed with injected dependencies. The
`Provider` itself stays an interface, because otherwise the agent would be tied
to a concrete type (see the deviations of stage 4).

## Starting state

Done:
- `display.Display` — interface, 4 implementations (TUI/Terminal/Minimal/collector)
- `providers.Provider` — interface, there is a `mockProvider` in the agent tests
- `api.ClientFromContext` — swapping `*http.Client`, tests on `httptest`

To fix:
- `agent` imports `display` (`agent.go:12`, `run_once.go:10`, `fallback.go`)
- no connectors — there is a `switch` in `providers/config.go:250-330`
- `api.StreamChat/StreamAnthropic/StreamGemini` are package-level functions → `noanthropic`/`nogemini` build tags as a workaround
- `api/client.go` — dead, parallel data model
- global singletons: `providers.providers`, tool registry, `SetSubAgentRunner`
- `agent/fallback.go:36` calls the global `providers.FindModel`
- own `&http.Client{}` in `internal/mcp`, `internal/connect`

---

## Stage 0 — safety net (0.5d) — DONE

- [x] `go test ./... -count=1` green before starting
- [x] characterization tests of `dynamicProvider.Stream` for 3 apiTypes through `httptest`
- [x] golden files with the JSON sent (wire format protection)

`providers/wire_golden_test.go` + `providers/testdata/wire_{openai,anthropic,gemini}_{request,events}.golden.json`.
The golden freezes the method, path, header whitelist and body; the second golden — the `stream.Event` sequence.
Regeneration: `go test ./providers/ -run TestWireGolden -update`.
The file has `//go:build !noanthropic && !nogemini` — to be deleted in stage 3 together with the stubs.

## Stage 1 — inverting `agent → display` (0.5d) — DONE

- [x] `agent.Sink` = copy of today's `display.Display` (`agent/sink.go`)
- [x] signatures in `agent.go` / `run_once.go` / `fallback.go` / `run_tools.go` switched to `Sink` (7 places)
- [x] zero changes in `display/` (structural typing does the job)
- [x] verification: `go list -deps ./agent | grep display` → empty

The goldens from stage 0 passed without `-update` — proof that behavior is untouched.
`display.Display` stays for call sites; to be removed eventually after stage 6.

## Stage 2 — `connector` package (1.5–2d) — DONE

- [x] `connector/connector.go`: `Connector`, `Endpoint`, `Factory`, `Registry` (a value, not a global)
- [x] `connector/openai.go` + moving `RichMessagesToChat`
- [x] `connector/anthropic.go` + `RichMessagesToAnthropic` (`ConvertToolsToAnthropic` stayed in `api/` — `anthropic_client.go` uses it too and it has a stub under build tags; moving it would require changes in `api/`, i.e. going beyond the stage)
- [x] `connector/gemini.go` + `RichMessagesToGemini`, `convertToolsToGemini`
- [x] connector bodies at first only call today's `api.StreamX` (no HTTP change)
- [x] `dynamicProvider.Stream` shortened to: URI → key → `registry.New` → `conn.Stream` (125 → 39 lines)
- [x] golden files from stage 0 still pass **without** `-update`

The canonical message types (`Message`, `ContentBlock`, `Request`) now live
in `connector`; `providers` keeps aliases with the `=` sign, so `agent/`, `session/`,
`display/`, `tools/` and `main` did not need A SINGLE change. The `api/` package is untouched.

The only deliberate micro-change in behavior: sending `stream.StreamError` is now
uniform and does a `select` on `ctx.Done()`. Previously the openai branch blocked without
`select` (anthropic and gemini already had it). Visible only with an already cancelled ctx.

## Stage 3 — `HTTPDoer` (1d) — DONE

- [x] `type HTTPDoer interface{ Do(*http.Request) (*http.Response, error) }` (`api/api.go`)
- [x] `api.StreamX(...)` → methods `ChatStreamer` / `AnthropicStreamer` / `GeminiStreamer`,
      each with fields `HTTP HTTPDoer` and `Headers map[string]string`
- [x] `ClientFromContext` as a fallback when `Endpoint.HTTP == nil` — **the fallback STAYS until stage 4**
- [x] `connector.Endpoint.HTTP` and `.Headers` actually consumed (they were dead fields after stage 2)
- [x] injectable client in `internal/connect/{connect,modelsdev}.go` (`internal/mcp/http.go` already had the field)
- [x] remove the dead `api/client.go` (+ `chat_client.go`, `anthropic_client.go`,
      `gemini_client.go` and their two stubs — 1070 lines in total of an abandoned
      parallel `Streamer`/`StreamRequest`/`*Client` implementation)
- [x] remove the dead `tyciconfig.ProviderURI.FullEndpoint()`
- [x] `default:` in the old switch confirmed dead — `tyciconfig.Parse`
      normalizes every unknown scheme to `openai` (`uri.go:45-51`, covered by
      `TestParseURI_table`). The openai fallback in `providers.kindFor` **removed entirely**
- [x] remove `api/anthropic_stub.go`, `api/gemini_stub.go`
- [x] build tags moved from `api/` to the `connector/` level; per-kind registration
- [x] goldens from stage 0 still pass **without** `-update`

### Deviations from the plan (deliberate)

**`ClientFromContext` and `HTTPClientKey` stay.** The plan said "then remove" — narrowed.
The context is today the ONLY way to inject a client: the real consumer is the subagent's
isolated connection pool (`tools/subagent.go:408-415`) plus the golden tests. Removing the
fallback requires someone to supply an `HTTPDoer` when building the provider — and the provider
becomes a struct only in stage 4. Client selection: `if s.HTTP != nil { s.HTTP } else
{ ClientFromContext(ctx) }`, a comment at `api.doer()` points to stage 4.

**Build tags do NOT disappear, they move.** The plan assumed "remove build tags",
but `make minimal` must still physically contain no anthropic/gemini code — otherwise
"minimal" stops being minimal. Instead:

- `connector/anthropic.go` + `connector/gemini.go` (and their tests) get the tags
  `!noanthropic` / `!nogemini`; `api/anthropic{,_types}.go` and `api/gemini{,_types}.go` too,
- registration in the default registry is assembled per-kind: each connector file appends
  its factory from `init()` to `connector.builtinFactories`, so a tag that removes the file
  removes the registration too — there is no single place listing the trio,
- `Makefile` unchanged (`minimal` = the same two tags).

Verified with `go tool nm`: a `-tags "noanthropic nogemini"` build contains not a single
anthropic/gemini symbol (the full build has 7), and the binary is ~72 kB smaller.

**A trap this uncovered:** after stage 2 `providers.kindFor` used `Registry.Has`
with a fallback to openai. In the minimal build an `anthropic://` URI would then silently go through
the openai connector — an Anthropic-style request to the chat-completions endpoint, i.e.
a silent wrong request instead of a readable error from the stub. Fixed: `connector.IsKnownKind`
+ `connector.ErrExcluded` give exactly the old message ("anthropic support excluded at
build time (rebuild without -tags noanthropic)"), and the openai fallback is gone entirely.
Tests: `TestDynamicProviderKindFor_*` in `providers/provider_test.go` (they work without tags,
because they inject their own registry).

**`providers/wire_golden_test.go` keeps the `!noanthropic && !nogemini` tag.** The plan
assumed deleting it together with the stubs. Not possible: the file asserts the anthropic
and gemini goldens, which a build without those connectors by definition cannot produce.

**`connector.Endpoint.Headers` consumed** (the plan did not mention it). Headers are
set AFTER the defaults, so they can override them; the map is always empty today, so the bytes
on the wire do not change. Proof that the field is not decoration:
`connector/endpoint_http_test.go`.

**Pre-existing debt fixed along the way:** `go test -tags "noanthropic nogemini" ./api/`
compiles again. The helpers `testCtx()` and `as()` moved from tagged files to the
untagged `api/api_test.go`, and the `TestStreamGemini_*` tests were split out into
`api/gemini_test.go` under `//go:build !nogemini`. All four tag combinations checked:
build + vet + test.

**`internal/connect`:** three `&http.Client{}` (2× `connect.go`, 1× `modelsdev.go`)
replaced by an `HTTPDoer` parameter on the fetchers and one `defaultHTTPClient` in
the CLI calls. No `Timeout`, exactly like the literals they replaced.
`fetchModelsDev` went from `client.Get` to `NewRequest`+`Do` (the same request).

Zero changes in observable behavior in the full build. The only behavior change
concerns the minimal build and is a fix for the trap described above: an unknown api_type
(unreachable through `parseURI`) now yields an `unsupported api_type` error instead of a silent
redirect to the openai connector.

## Stage 4 — provider as a struct (1d) — DONE

- [x] `providers.Provider`: the implementation becomes an explicitly constructed struct
      (`Catalog` as a value, `AuthSource`, `connectors`, `http`) — **the `Provider`
      interface stays**, see deviations
- [x] remove `api.ClientFromContext` / `api.HTTPClientKey` — the provider injects
      `HTTPDoer` into `connector.Endpoint.HTTP`. The isolated pool moved
      from `tools/subagent.go` to `main.go:withIsolatedPool`; injections
      in `providers/provider_test.go` and `providers/wire_golden_test.go` go through `Deps.HTTP`
- [x] `AuthSource` as an interface (`LiteralAuth` / `AuthFile` / `EnvAuth` / `AuthChain`)
- [x] `providers.Default` stays for the CLI; `Catalog` is a value, tests build their own
- [x] goldens from stage 0 still pass **without** `-update`

### Deviations from the plan (deliberate)

**`providers.Provider` STAYS an interface.** The "interface → struct" from the plan header
narrowed to the implementation: `dynamicProvider` stops reaching for `defaultConnectors`,
`connect.GetKey` and `os.Getenv`, and gets them through `NewProvider(name, entries, Deps)`.
If `Provider` stopped being an interface, `agent.Run(ctx, p providers.Provider, ...)`
would tie the agent to a concrete type — the exact reverse of the goal of the refactor — and would break
the fakes from `main_resolve_test.go` and `agent/agent_test.go`. Swappability stays
at the connector level, as the diagram says.

**`WithHTTP` does NOT go into the `Provider` interface.** It is a method of the concrete type plus
an optional interface `providers.HTTPInjector`. Otherwise every provider fake would have to
implement an HTTP concern that the agent has no right to know about. `main.go` does a
type assert; a provider without this method stays untouched and gets today's
"no isolation" behavior — which is what the fakes have today.

The cost of this compromise, deliberate: transport injection is invisible in the
`Provider` contract, so an implementation that is not a `*dynamicProvider` silently gets no
isolation and nothing will detect this at compile time. The gap in stage 5 (the fallback provider)
could have appeared for exactly this reason. Likewise `Deps.HTTP == nil` leads to the global
`api.defaultClient` — the provider is not the full owner of its transport, because
the normal production path deliberately keeps one client per process (connection reuse).
"Everything injectable" is therefore true for a caller who asks for it.

**`WithHTTP` returns a copy, never mutates the receiver.** Parallel subagents
(`subagent(tasks=[...])` → `runTasks` → goroutine per task) share a single provider
value; mutation would leak one child's pool into another's requests.
Test: `TestWithHTTP_ReturnsCopy`, `TestWithIsolatedPool_FreshClientPerCall`.

**Granularity of the isolated pool unchanged.** Today one `*http.Client` per
`runSingleTask`. After the move: one per entry into `agentRunner.run`, and `run` is
called exactly once per `RunTask`/`RunTaskWithSystem`, i.e. once per `runSingleTask`.
Parallel `subagent(tasks=[a,b,c])` still creates three pools.

**No real semantic difference after taking the client out of the context.** The plan suspected
that the client from the context covered *any* call of the `api` layer in the child's run,
and after the change it covers only the provider's streams. Verified: `api` performs HTTP
only in three places (`chat.go:147`, `anthropic.go:120`, `gemini.go:77`), all
through `doer()`, and the only non-test constructors of streamers are the three connectors
built exclusively by `dynamicProvider.Stream`. The other HTTP consumers in the child's
run (`tools/web.go`, `internal/mcp`, `internal/connect`) always had their own
clients and never read the context key. The set of covered calls is the same.

**`doer()` keeps the guard against a typed-nil `*http.Client`.** The deleted
`ClientFromContext` had `cl != nil`; `Deps.HTTP` is an interface, so
`Deps{HTTP: someNilClient}` is easy to produce. Without the guard it would be a panic
in `net/http` instead of the former fall back to `defaultClient`.

**`api.defaultClient` STAYS.** It is a default, not a context read: a provider with `http == nil`
says "I have no client of my own" and that is the normal production path.

**`providers/providers_test.go` did not need rewriting.** The plan said "844 lines" — a number
inherited from before stage 2. The file has 282 lines today and covers `LoadConfig` / `MustLoadConfig` /
`parseURI` / `parseModel`, i.e. things untouched by this stage. `Catalog` tests
were added; nothing was removed.

**Gemini wire-format bugs moved OUT of stage 4** (see "Bugs found at stage 0").
The goldens are the safety net of this refactor — deliberately breaking them in the same stage
would remove the ability to tell "the refactor broke something" from "we changed behavior on purpose".

**Test names: 6 transformations, 0 deletions.** `TestClientFromContext_{DefaultClient,
OverrideFromContext,NilClientInContext}` → `TestDoer_{NoInjectionUsesDefaultClient,
InjectedClientWins,TypedNilClientUsesDefaultClient}` (the same three guarantees through the new
injection path). `TestChatStreamer_{HTTPFieldWinsOverContext,
NilHTTPFallsBackToContext}` → `...OverDefaultClient` / `...ToDefaultClient` and
`TestEndpointNilHTTPUsesContextClient` → `...UsesDefaultClient` — the names themselves became
untrue once the context was gone.

**The old note "`config.go:Stream` does `for _, e := range p.entries`" is outdated.**
Stage 2 turned that loop into `findEntry`, which indexes `p.entries[i]`. There is nothing
to clean up; the note was deleted.

**Stage 5 deliberately untouched.** `agent/fallback.go` still calls the global
`providers.FindModel`, and `providers.WithProvider` / `ProviderFromContext` stay —
that is the scope of stage 5.

### Verification

- `go build` + `go vet` + `go test ./... -count=1` green in all four
  tag combinations (none, `noanthropic`, `nogemini`, `noanthropic nogemini`),
- `gofmt -l .` empty,
- `git diff --stat d724940..HEAD -- providers/testdata` **empty** — the wire format survived,
- `grep -rn "ClientFromContext\|HTTPClientKey" --include="*.go" .` → nothing (also in comments),
- `go tool nm` on a `-tags "noanthropic nogemini"` build: zero anthropic/gemini symbols
  (full build: 25 unique). The same numbers on a binary built from `d724940`,
  i.e. no regression relative to stage 3. Note: stage 3 recorded "the full build has 7" —
  a different measure (`grep` on names only vs. on whole `nm` lines), not a change of state.

## Stage 5 — fallback outside the agent (0.5d) — DONE

- [x] `Config.FallbackModels []string` → `Fallbacks []connector.ModelClient` resolved by the caller
      (`commands.go:resolveFallbacks`, `main.go:resolveModelClient`, `internal/workflow/engine.go`)
- [x] `agent/fallback.go` stops calling `providers.FindModel` — it iterates `cfg.Fallbacks`,
      already resolved; the only possible error in the loop is a failed `Stream`, not "not found"
- [x] `providers.WithProvider`/`ProviderFromContext` + `WithModel`/`ModelFromContext`
      (`providers/context.go`, deleted) → `connector.WithModelClient`/`ModelClientFromContext` —
      one value in the context, because `ModelClient` already carries its model
- [x] verification: `agent` does not import `providers` (`go list -deps ./agent` — see below)
- [x] **acceptance criterion: the child's fallback provider also gets an isolated pool.**
      `main.go:withIsolatedPool` now binds the primary provider AND every fallback to ONE
      private `http.Client` (shared within a single child run, because primary
      and fallback never run in parallel). `agentRunner.run` passes through the same
      wrapper the fallback list, which is always empty today (named subagents do not
      have `GetFallbackModels` wired up yet — that stays out of scope, see deviations),
      so the mechanism is ready before anyone actually uses it.
      Tests: `TestWithIsolatedPool_WrapsFallbacksWithPrimary`,
      `TestWithIsolatedPool_FallbackPoolDistinctAcrossChildren`.

### Deviations from the plan (deliberate)

**Stages 4 and 5 of the plan ("suggested commit sequence") merged into one commit
instead of two.** The plan proposed: commit 4 — `Run` takes a `ModelClient`
+ `fallback.go` stops calling `FindModel`; commit 5 — separately switch the
context carrying. It cannot be split without an intermediate commit that
would be red or force a temporary dual-write: `agent/run_once.go` calls
`providers.WithProvider(ctx, p)`, where `p` is a `providers.Provider` — at the
moment `Run` starts accepting a `ModelClient` (which is not a
`providers.Provider`), `run_once.go` physically has nothing left to call
`WithProvider` with. Writing/reading the context must therefore change in the same
step as the `Run` signature — hence one commit
(`250481e`, "agent: fallback resolved by the caller, ModelClient in the context", translated from the original Polish subject)
covering both points of the plan.

**`session/session.go` also stopped importing `providers`, even though the plan
("Design decisions ALREADY MADE") said explicitly: "Keep the aliases in
providers — the CLI and session still use them."** Discovered while verifying
`go list -deps ./agent`: `agent` imports `session` (the `*session.Session` type
in `Config.Session`), and `session.go` imported `providers` solely for the
`RichMessage`/`ContentBlock` aliases (the same types as `connector.Message`/
`ContentBlock` — `providers` only re-exports `connector`). Without this change
"`agent` does not import `providers`" would be true only for direct imports,
and `go list -deps ./agent | grep providers` would still print something — so the
stage's headline criterion would be false. Fix: the same mechanical rewrite to
`connector.Message`/`ContentBlock` that `agent/` got in commit 3 — zero
behavior change (the alias is the same type), the session/ test needed no
modification. `providers` remains the owner of the aliases; `session` now prefers its
own, direct name, just like `agent`.

**`resolveModelClient` (formerly `resolveProviderModel`) got a new branch, with no
counterpart in the pre-stage code.** The old code held `providers.Provider`
and `model string` as TWO independent values in the context, so a subagent with
an explicit bare-name override (`model` different from the parent's model, without `/`) simply
got `(parentProvider, newModel)` — the provider knew nothing about any
specific model, so there was nothing to rebuild. A `ModelClient` carries
its model permanently, so when the override differs from `mc.Model()`,
`resolveModelClient` must recreate a new `ModelClient` on the same provider
through `providers.GetProvider(mc.Provider())` (a read from the global catalog by
name). Functionally identical behavior — the parent's provider, a different model —
but the code path is new, because previously there was nothing to cover it with: no
existing test used this case (the override in tests and tools/
was always either `""` or a full `"provider/model"`). Added test:
`TestResolveModelClient_BareOverrideDifferentModelReusesProvider`.

**Two dead fields `fallbackState.active`/`.fullModel` removed along the way.**
Set in `agent/fallback.go`, never read (`grep` confirms it).
Unavoidable: when replacing `provider+model+fullModel` with a single field
`mc connector.ModelClient` every field of the struct had to be touched; this
is not a separate "improvement on the side", just a consequence of the mandatory rebuild.

**Pre-existing debt noticed and DELIBERATELY untouched:** `agent.Config.ProviderName`
is written in six places (`commands.go`, `interactive.go` ×2,
`tui_mode.go` ×3) and is read nowhere in the whole repo — a
write-only field from before this stage. Not fixed here: it has nothing
to do with the scope of Stage 5, and "do not commit unrequested fixes" is
a hard constraint of this task.

**`Config.Model` and `Config.ProviderName` stay in the struct, even though `agent`
no longer needs them internally** (`runOnce`/`fallback.go` read
`mc.Model()`/`mc.Provider()`). Reason: callers (`prompt_mode.go:29`,
`interactive.go`, `tui_mode.go`) read/write `cfg.Model` for their own purposes
(session name, model switching), independent of how `Run` consumes it.
Removing the field would break those call sites with no benefit.

Resolution of both postponed to **before stage 6** — see "Cleanup BEFORE
`Conductor`" in the stage 6 section. The reason it does not stay here forever:
`Conductor` carries `agent.Config` over into a new API, and a field the agent does not
read looks like a contract in a new API. `ProviderName` does not even have
the excuse `Model` has — nobody reads it, neither the agent nor the callers.

### Verification

- `go build` + `go vet` + `go test ./... -count=1` green in all four
  tag combinations (none, `noanthropic`, `nogemini`, `noanthropic nogemini`),
- `go test -race ./agent/ ./providers/ ./tools/ .` green,
- `gofmt -l .` empty,
- `git diff --stat a04f9a8..HEAD -- providers/testdata` **empty** — the wire format survived,
- `go list -deps ./agent | grep decodo/tyci/providers` → **empty** (headline proof of the stage),
- test names: 6 transformations 1:1 (`TestResolveProviderModel_*` →
  `TestResolveModelClient_*`), 16 added (new packages `connector`/`providers`
  + one new `resolveModelClient` case + two fallback isolation tests),
  0 deletions without a counterpart.

## Stage 6 — frontend as a driver (2d) — DONE

**Resolved BEFORE this stage:** `Provider.FreeModels()` was dead in production
(the only non-test implementation returned `nil` unconditionally).
**Decision: cut out, not implemented** — see "Cleanup before `Conductor`"
below. The method does not enter the `Conductor` design.

### Cleanup before `Conductor` — DONE

`Conductor` is designed around `agent.Config` and `connector.ModelClient`. Three
of the items below are fields and abstractions that stage 5 left in the state "exists, but
nobody reads it". Carrying them into the new API would have entrenched the mistake, so
the order was: cleanup first, then `Conductor`.

- [x] **`agent.Config.Model` — the agent ignored it.** Field removed from `Config`.
      The model is solely a property of the `connector.ModelClient` passed to `Run`
      (`mc.Model()`); callers who need the name for their own purposes
      keep their own variable. `runPrompt` got an explicit `modelName` parameter
      (it read `cfg.Model` when creating the session and when building the client);
      `commands.go` / `interactive.go` / `tui_mode.go` already had `provider` +
      `modelName` next to `cfg`, so the writes to `cfg` were pure duplication;
      `main.go` and `internal/workflow/engine.go` only wrote. Two sources of truth →
      one. The comment "the agent does not read this" was deliberately NOT left in place —
      that is exactly the state we were removing.
      Commit `8d90b5d`.
- [x] **`agent.Config.ProviderName` — dead and misleading.** Removed in the same
      commit. Session metadata takes `mc.Provider()` (`run_once.go`).
- [x] **`Provider.FreeModels()` cut out.** The method dropped out of the interface, out of
      `dynamicProvider` and out of three test fakes; the second loop in
      `Catalog.FindModel` disappeared (together with a comment that described it as if it were live)
      along with four dead loops in the CLI. Conservative *by construction* — every
      use iterated over `nil`; per-use proof in the description of commit `76a8629`.
      The only place with an effect on control flow, `provider list --models`, had
      the condition `len(models) == 0 && len(freeModels) == 0`, which reduces to
      `len(models) == 0`, so the "(no models)" message appears exactly where
      it did before.
- [x] **`providers.Provider` is now solely a catalog interface.**
      `Stream` dropped out of the interface; `Provider` = `Name` + `IsConfigured` +
      `Models` + `Client(model) connector.ModelClient`. The only way to send
      a request is `connector.ModelClient`. Commits `bec3622` (the factory becomes
      a method) and `60c32db` (`Stream` taken off the interface).
- [x] **`HTTPInjector` — three links → one.** `providers.HTTPInjector` removed
      entirely, `dynamicProvider.WithHTTP` → unexported `withHTTP`
      returning the concrete type. One link stays, `connector.HTTPInjector`
      on `modelClient`, and one intentional assertion in
      `main.go:withIsolatedPool` (`ModelClient` fakes do not satisfy it and are supposed
      to pass through without isolation, as before). So that it cannot silently stop
      working for the production path, `providers/client.go` has
      `var _ connector.HTTPInjector = (*modelClient)(nil)` — a runtime failure
      turned into a build failure. Commit `60c32db`.

#### The `Provider` split design and why it is this way

`Provider` **stays an interface** (a constraint from stage 4: a struct would break
every fake and drag the concrete type into signatures). The split looks like this:

```
Provider (catalog)                 connector.ModelClient (transport)
  Name() / IsConfigured()            Provider() / Model()
  Models()                           Stream(ctx, Request)
  Client(model) ──────────────────▶  (+ optionally connector.HTTPInjector)
```

Key decision: **the `ModelClient` factory is a `Provider` method, not a
package-level function.** The former `providers.Client(p Provider, model string)` is gone.
The reason is precisely about silent failure: a function taking the `Provider` interface,
which no longer has `Stream`, would have to *find the transport behind* the interface, i.e.
through a type assertion — and a failed assertion in such a place degrades without
an error. As a method it is checked by the compiler: every implementation of
`Provider` must be able to issue its client and itself decides what that client
can do. That is also why the test fakes are *lighter* today, not heavier —
a catalog fake does not inherit a transport it did not ask for.

Consequence for the HTTP chain: `modelClient` holds a `*dynamicProvider` (the concrete
type), so `modelClient.WithHTTP(h)` = `c.p.withHTTP(h).Client(c.model)` —
both hops are static, there is nothing to fail to match. One assertion remains, in
`main.go`, and it is the only place where "no isolation" is the correct
answer (fakes). The `var _` in `providers/client.go` guarantees that it is never
the answer for a production client.

As a result `dynamicProvider.Stream` went out of reach: the method stayed on an
**unexported** type, so from outside the `providers` package there is no path to a
stream other than `connector.ModelClient`. Tests in `providers/` that built
a provider through `NewProvider` now stream through `p.Client(model).Stream(...)`,
i.e. through the production path — this also applies to `wire_golden_test.go`.

#### Deviations from the plan (deliberate)

**The tasks "split `Provider`" and "shorten the `HTTPInjector` chain" could not be
separated into two commits along the task boundary.** The plan envisioned a separate commit for
each. But the boundary is not a seam that compiles: the moment
`Provider` loses `Stream`, `modelClient` must hold the concrete type (holding
`Provider` + an assertion to some "streamer" would recreate exactly the silent
failure mode we are removing), and a `modelClient` with a `*dynamicProvider` field
invalidates the signature of `providers.HTTPInjector` (`WithHTTP(HTTPDoer) Provider`) —
Go has no return-type covariance, so the interface stops being satisfied
by anything and must disappear in the same commit. The split therefore went along a
seam that *can* be compiled: `bec3622` introduces the new factory
(`Provider.Client`), `60c32db` removes the old path (`Stream` from the interface +
`providers.HTTPInjector`). Each of the two commits is green on its own.

**`dynamicProvider.Stream` was left exported.** Taking it off the interface
is enough: the type is unexported and `NewProvider` returns `Provider`, so
the method is unreachable from outside the package. Renaming it to `stream` would only add
a readability clash with the imported `stream` package.

**Granularity and semantics of the isolated pool unchanged.** `withIsolatedPool`
still gives one `*http.Client` per entry into `agentRunner.run` (i.e. per
`RunTask`/`RunTaskWithSystem`), shared by the main model and all its
fallbacks. Both fallback isolation tests from stage 5 pass with no changes in
the assertions; only the fake changed — `recordingInjector` records the injected
client at the `ModelClient` level, not `Provider`, because `withIsolatedPool` sees
only `ModelClient`s and that is the level at which its contract exists.

**Test names: 1 transformation 1:1, 1 addition, 2 deletions (net −1).**
`TestNewProvider_ImplementsHTTPInjector` → `TestProviderClient_ImplementsHTTPInjector`
(the same guarantee moved from the provider to the client, plus a check that the
injected client actually lands in `Endpoint.HTTP`).
`TestClient_WithHTTPNoopWhenProviderIsNotInjector` **deleted with no successor**:
the branch it described ceased to exist (every `modelClient` wraps a
`dynamicProvider`, which is always an `HTTPInjector`). The guarantee "a `ModelClient`
without `HTTPInjector` passes through `withIsolatedPool` untouched" is held by
`TestWithIsolatedPool_PassesThroughNonInjector` in `main_resolve_test.go`.
The `recordingProvider` fake (not a test) removed as unnecessary — after the split
a `Provider` fake issues ITS OWN client, so it would not have touched `modelClient`
even once; `TestClient_{ProviderAndModel,StreamForcesBoundModel}` now go through
a real provider with a recording connector.

**`TestCatalog_FindModelBareName` lost the "freebie" case** — the assertion
described the removed `FreeModels` loop. The test and its two remaining assertions
stay.

#### Verification

- **4/4 commits green on their own.** Checked in a throwaway
  `git worktree --detach`, commit by commit: `go build ./... && go vet ./... &&
  go test ./... -count=1 && gofmt -l .` (empty) + `go build` with the tags
  `noanthropic`, `nogemini`, `noanthropic nogemini`. Worktree removed.
- `go build` + `go vet` + `go test ./... -count=1` green in all four
  tag combinations (none, `noanthropic`, `nogemini`, `noanthropic nogemini`),
- `go test -race ./agent/ ./providers/ ./tools/ .` green,
- `gofmt -l .` empty,
- `git diff --stat ace8e16..HEAD -- providers/testdata` **empty** — the wire format
  survived, not a single `-update`,
- `go list -deps ./agent | grep decodo/tyci/providers` → **empty**
  (headline criterion of the whole refactor),
- `grep -rn "FreeModels" --include="*.go" .` → two comments describing the removal,
  zero code; `grep -rn "providers.HTTPInjector"` → one historical comment
  (description of the shortened chain in `providers/client.go`), zero code,
- test names: 1027 → 1026 (`comm` on sorted `func Test*` lists before
  and after): 1 transformation 1:1, 1 added, 2 deleted — itemized above.

#### Found along the way, DELIBERATELY untouched

- **`subagentDefaultMaxIterations` in `main.go:125` is a dead constant** —
  defined as an alias of `tools.DefaultSubagentMaxIterations` and not used
  anywhere (`tools.ResolveMaxIter` does the same on the `tools/` side). Not touched:
  out of scope.
- **`display.ProviderModels` today receives only paid models** — after cutting out
  `FreeModels` there is no longer a path by which the TUI could get a model marked
  as free. If "free models" ever return, they must return as a property
  of a catalog entry (`ModelEntry`), not as a second interface method — the previous
  shape rotted precisely because nobody had anything to populate it with.

### `Conductor` — DONE

- [x] `Conductor`: `conversation` + `cfg` + `ModelClient` + session (`conductor/conductor.go`)
- [x] API: `Submit(ctx, prompt)`, `Interrupt()`, `SwitchModel(spec)` — plus `Resume`
      and state operations, see below
- [x] moving the logic out of `interactive_agent.go`, `tui_mode.go`, `prompt_mode.go`
      and the construction point in `commands.go`
- [x] TUI/console only call methods and render events
- [x] smoke test: headless driver with no UI at all
      (`TestConductor_HeadlessConversation`)

#### API shape

```go
type ModelResolver interface {
    Resolve(spec string) (connector.ModelClient, error)
}

type Options struct {
    Client      connector.ModelClient // the model the conversation starts on
    Sink        agent.Sink            // where the loop's events go
    Config      agent.Config          // Config.Session = the log Conductor becomes the owner of
    Resolver    ModelResolver         // optional; without it SwitchModel returns ErrNoResolver
    History     []connector.Message   // seeding the conversation (resume)
    SessionPath string                // empty = no persistence
    WorkDir     string                // empty = os.Getwd() at the moment the file is opened
}

func New(opts Options) *Conductor

func (c *Conductor) Submit(ctx context.Context, prompt string) (stream.Usage, error)
func (c *Conductor) Interrupt()
func (c *Conductor) SwitchModel(spec string) error
func (c *Conductor) Resume(path string, msgs []connector.Message, usage stream.Usage) error

func (c *Conductor) Model() string
func (c *Conductor) Provider() string
func (c *Conductor) Usage() stream.Usage
func (c *Conductor) Messages() []connector.Message
func (c *Conductor) SetHistory(msgs []connector.Message)
func (c *Conductor) ClearHistory()
func (c *Conductor) ResetUsage()
func (c *Conductor) Session() *session.Session
func (c *Conductor) SessionPath() string
func (c *Conductor) EnsureSession() *session.Session
func (c *Conductor) EndSession(status string, exitCode int)
```

The boundary runs like this: **`Conductor` says what happened; the frontend decides how
it looks.** `Conductor` holds the conversation history, `agent.Config`, the current
`ModelClient`, the session log, `stream.Usage` accumulation, lazy creation of the session
file, `agent.Run` and turn cancellation. The frontend keeps everything that is
a presentation decision or terminal property: error texts and their `\n`,
`display.End()`, exit codes, transcript replay, the `/resume` picker, window title,
model listing, and also **who listens for SIGINT and ESC** — `Interrupt()`
says only "abort the current turn", not "handle the keyboard".

`Conductor` does not import `providers`. Switching the model goes through
`ModelResolver` — an interface declared on the consumer side, exactly like
`agent.Sink` and `connector.HTTPDoer`. The implementation (`main.catalogResolver`)
sits in the CLI, because the catalog is owned by the CLI.

#### Resolving the `SwitchModel` / global catalog trap

The stage note said that `main.resolveModelClient` must reach for the global
`providers.GetProvider(mc.Provider())`, because from a `connector.ModelClient` there is no way
back to the catalog, and that `SwitchModel` would hit the same problem. **It does not**,
and not by accident: `SwitchModel` receives the full `provider/model` specification
from the user, so it does not need to recover anything from the current client —
`ModelResolver.Resolve(spec)` returns a ready client and that is enough. The catalog
stays on the `main` side, `Conductor` sees only a one-argument function.
The `resolveModelClient` case (a subagent with a bare model name that is supposed to
inherit the parent's provider) is different — there the specification is *incomplete* —
and therefore stays in `main` untouched. It is the same boundary from two sides:
incomplete specifications need the catalog, so whoever has the catalog resolves them.

#### Deviations from the plan (deliberate)

**Three behavior changes, all forced by collapsing to a single state owner.
None is cosmetic, so all are listed here.**

1. **`/resume` in the console actually rebinds the session the agent sees.**
   `interactive.handleResume` set `s.sessionPtr`, but **never**
   `s.cfg.Session`. After resuming (if the user had already typed anything
   earlier) the agent kept writing to the abandoned file, `close()` wrote
   `session_end` to the old one, and the screen showed the path of the new one. With
   a single field in `Conductor` such a divergence is not representable. A
   pre-existing bug, overturned by the unification — it could not be "preserved".
2. **`/resume` in the console closes the abandoned session with `WriteSessionEnd` instead of
   a bare `Close()`** — i.e. the way the TUI has always done it. Same
   cause: there is a single `Conductor.Resume`.
3. **TUI: usage from a turn interrupted with ESC is added to the total.** The `ESC` branch
   in `runTUI` was the only one that dropped partial usage (the `resultCh` branch added
   it even on cancellation). `Conductor` sums in one place, in `Submit`,
   so the inconsistency disappears. Visible only in the `usage` field of the
   `session_end` event.

**Micro-shift: interrupt watchers are armed slightly earlier.**
Until now the console appended the user message and opened the session file *before*
`startInterruptWatcher`; now `Submit` does it, so the watcher is already
armed. The window is a few microseconds for writing one JSONL line; the effect is
that a Ctrl+C hitting exactly that window cancels the turn instead of killing the process.

**`/new` STILL works differently in the console and in the TUI — on purpose.** The console only
clears the conversation (`ClearHistory`), the TUI additionally ends the log and zeroes usage
(`EndSession` + `ClearHistory` + `ResetUsage`). That is a pre-existing difference that this
stage had no right to unify. The only change is that it is now
**visible** — three calls next to one — instead of buried in two
copies of the loop.

**The `IsConfigured` check on model change was split by a flag.**
`catalogResolver{requireConfigured: true}` for the console (`/model` refuses
a provider without a key and says how to add one), `catalogResolver{}` for the TUI
(the model list is already filtered by `auth.json`, and silently rejecting a
favorite model would look like a dead key). The only side effect:
`/resume` in the console, which tries to return to the model stored in the session, will not
switch to a provider that has lost its key in the meantime — it will stay on a
working model instead of switching to a dead one.

**`ensureLazySession` + `normalizeCWD` moved to `conductor/` in full,
with tests.** Lazy creation of the session file is a property of `Conductor`, not
`main`. This is the only place where `Conductor` writes to its own stream
(`os.Stderr`) instead of to the `Sink`: the warning "could not open the log,
continuing without it". Deliberately left literally as it was — it is the
same class of diagnostics that `agent/session_log.go` has always emitted, not
a presentation decision. A candidate for an injectable hook, should a
frontend ever appear for which stderr is a nuisance.

**`main.go:agentRunner` and `internal/workflow/engine.go` STAY with
`agent.Run`.** Both are headless callers for which `Conductor` gives nothing:

- `agentRunner.run` builds one message, calls `agent.Run` once and normalizes the
  result to `tools.ErrSubagentTruncated`. There is no conversation to own (`msgs`
  lives for one run), no session, no model switching or interruption.
  Adoption would save three lines and add an allocation.
- `engine.sessionAwait` keeps `session.messages` in a Lua object that is
  serialized to Lua tables (`sessionMessages`, `sessionSave`, `sessionLoad`)
  and appends messages of **any role** (`sessionSystem` inserts `system`).
  `Conductor.Submit` appends only `user`, so adoption would require
  adding `AppendMessage(role, ...)` to the API — i.e. widening the contract for
  the one caller who has no frontend to separate anyway.

The criterion was: "if adoption simplifies — do it; if it gives nothing — leave it
and write down why". Here it gave nothing in both cases.

**Migration stages split into four commits, starting from the simplest driver.**
`conductor` (unused) → `prompt_mode` → console → TUI. Each green on its own;
for three commits `main` kept its own copy of `ensureLazySession`, deleted
only when the last driver stopped using it.

#### Verification

- **6/6 commits green on their own.** Checked in a throwaway
  `git worktree --detach`, commit by commit: `go build ./... && go vet ./... &&
  go test ./... -count=1 && gofmt -l .` (empty) + `go build` with the tags
  `noanthropic`, `nogemini`, `noanthropic nogemini`. Worktree removed.
- `go build` + `go vet` + `go test ./... -count=1` green in all four
  tag combinations (none, `noanthropic`, `nogemini`, `noanthropic nogemini`),
- `go test -race ./conductor/ ./agent/ ./providers/ ./tools/ .` green,
- `gofmt -l .` empty,
- `git diff --stat 0ad2271..HEAD -- providers/testdata` **empty** — the wire format
  survived, not a single `-update`,
- `go list -deps ./conductor | grep decodo/tyci/providers` → **empty**
  (headline criterion of this stage),
- `go list -deps ./agent | grep decodo/tyci/providers` → **empty**
  (headline criterion of the whole refactor, untouched),
- test names: 1026 → 1043 (`comm` on sorted `func Test*` lists):
  **17 added, 0 removed, 0 transformations.** Four `TestEnsureLazySession_*`
  tests moved from `main` to `conductor` together with the
  code — same name, same file, different package, so `comm` does not see them.
- drivers: `interactive_agent.go` 99→62, `tui_mode.go` 372→320,
  `prompt_mode.go` 102→92, `interactive.go` 308→301, `cmd_interactive.go`
  533→483 (lazy session removed), `commands.go` 922→940 (+18: `Conductor`
  construction and comments at three `RunE`s). In total −135 lines in the drivers
  against +421 lines of the new, tested package.

#### Headless smoke test

`TestConductor_HeadlessConversation` runs a full conversation: user prompt
→ the model asks for a tool → the tool runs → the model answers. The collaborators
are a scripted `connector.ModelClient` (a fake yielding a fixed sequence of
`stream.Event`s per call), a `Sink` writing to slices and a map-backed
`ToolRunner`. **Not a single UI: no TUI, no terminal, no readline, no
`os.Stdout`.** The assertions cover exactly what was previously unreachable without
bringing up a frontend: two model calls (the second with the tool result appended),
tool execution with arguments sent through the stream, usage
summed from both turns, and the message roles in the conversation that `Conductor`
now owns (`user, assistant, toolResult, assistant`).

The fakes are local to the package. Stage 7 (`connector/connectortest`) will replace them —
building that infrastructure here would be doing stage 7 in a stage 6 commit.

#### Found along the way, DELIBERATELY untouched

- **A dead `if` in `runTUI`, ESC branch:** `if !errors.Is(res.err,
  context.Canceled) && res.err != nil { }` — the body has always been empty, with
  the comment "Real error, not just cancellation". Preserved literally: it is
  pre-existing code, not something this stage introduced.
- **`interactive.listAvailableModels` and `handleResume` are still 100 lines
  of formatting in `interactive.go`.** It is presentation, so it stays in the frontend
  per the split — but `listAvailableModels` could live next to
  `provider list` in `commands.go` instead of in the REPL file.
- **`Conductor` today has no protection against a parallel `Submit`.**
  The contract ("all methods except `Interrupt` from one goroutine") is described
  in a comment, not enforced. No frontend today breaks it; if an RPC frontend
  arrives, it will need either a mutex around the whole thing or a queue.

## Stage 7 — test connectors (1d) — DONE

The stage is split into 7A, 7B and 7C — all done, the split is described under the list.
What the stage deliberately did not do is listed in "What stage 7 did NOT do".

- [x] `connector/connectortest/fake.go` — a scripted `stream.Event` sequence.
      Configuration by struct literal (like `conductor.Options`, `agent.Config`,
      `connector.Endpoint`), modes: `Turns`, `OnExhausted`, `StreamErr`,
      `BlockUntilCancel`. `Fake` deliberately does NOT implement
      `connector.HTTPInjector` — the silent fallback of that interface is tested
      precisely with clients that lack it; a separate test guards this.
- [x] `flaky.go` — a decorator injecting 429 / 500 / EOF in the middle of a stream.
      Per-call failures (`Failures[n]`, `nil` = pass through to the wrapped
      client), two failure points: an error from `Stream` itself (the fallback path)
      and `stream.StreamError` after N events (the retry path). The error constructors
      are tied to the real consumer by a test on `api.IsRetryable`.
- **`record.go` / `replay.go` — deliberately will NOT be created.** An item crossed out
      in 7C, not overlooked. After 7B all tests run on `Fake`/`Flaky`
      and a recorder would not have a single consumer — and this refactor already cut out
      `FreeModels` precisely for being an interface method that nobody
      had anything to populate. Building a recorder with no user now would be
      repeating the same mistake in a new place.
      When it is worth coming back: when a scenario appears like "record a real conversation
      with a real key, replay it in CI without the key" — i.e. when coverage
      of the wire format of a live provider starts to be missing, which `Fake` by
      definition does not reproduce, because it sits *above* the transport.
- [x] cover the `Flaky` mid-stream failure path. The item originally read
      "rewrite the retry/fallback tests from `httptest` to `Flaky`" and
      in that wording it had no subject: no retry or fallback test
      ran through `httptest`. The agent tests always used in-process fakes,
      and `httptest` in `api/` serves tests of the HTTP layer itself (SSE parsing,
      headers, status codes), which `Flaky` will not replace, because it sits *above*
      the transport. Instead of a rewrite, two tests were added for what no fake
      could do — a failure after N emitted events:
      `TestRunFallback_MidStreamFailureAfterPartialText` (the fallback path) and
      `TestRun_RetryRecoversAfterMidStreamRateLimit` (the retry path, the first
      test of a successful retry in this package at all).
- [x] replace `mockProvider` from `agent/agent_test.go` with `Fake` — together
      with the other ten fakes in `agent/` and `bareModelClient`
      from `main_resolve_test.go`.
- [x] retire `api.defaultClientProvider` — a mutable global variable existing
      solely as a test seam (`api/api_test.go:757-763` swaps it and
      restores it in a `defer`). Safe today, because there is not a single
      `t.Parallel()` in `api/`, but that safety is by accident. In the end
      there was not even a need for a test connector: `httptest` speaks plain HTTP
      under `127.0.0.1`, so the real `defaultClient` reaches the server without
      any swapping.

#### What 7A did

The `connector/connectortest` package (`Fake` + `Flaky`, with its own tests) and the first
three consumers rewired immediately, so the API is not born in a vacuum:
`conductor/conductor_test.go` (the local `fakeClient` removed),
`tools/subagent_test.go`, `providers/providers_test.go`. Of the 16 hand-written
`connector.ModelClient` fakes, 13 remained (11 in `agent/`, 2 in
`main_resolve_test.go`).

#### What 7B did

All 11 fakes in `agent/` rewired to `connectortest.Fake` — of the 16 hand-written
`connector.ModelClient` fakes from before stage 7, two remained, both
in `main_resolve_test.go` and both for the reason for which `Fake` does not fit
(see below). Zero changes in production code apart from retiring
`defaultClientProvider`.

What came out while rewiring:
- **`Usage` in `Finish` is load-bearing, `Reason` is not.** `runOnce` reads only
  `e.Usage` and emits `Summary`/`Total` only when `hasUsage(lastUsage)` — a zero
  `Usage` decides whether the cost line appears at all, so every
  number stands explicitly in the `Turns` literal at the call site. `Finish.Reason`
  is read nowhere, so the difference between `"stop"` and `""` is cosmetic.
- **The `planGuard*Provider`s could be rewired.** The fear that they react to the content
  of the request did not materialize: they decided solely by call number.
  Their final calls closed the channel without any event, which is written
  explicitly as `OnExhausted: []stream.Event{}` — omitting the field would give a bare
  `Finish`, which is something else.
- **Two fakes answered the same way to EVERY call**
  (`countingTextProvider`, `alwaysToolProvider`), not just the first. Their
  script sits in `OnExhausted` with empty `Turns`, because `OnExhausted`
  applies from turn zero.
- **`Fake.Calls()` counts exactly what `p.calls` and `callCount()` counted** —
  every entry into `Stream`, regardless of result.
- Along the way the dead helper `newFailingProvider` (unused) disappeared.

`main_resolve_test.go`: `bareModelClient` rewired to `Fake` and this is a
strengthening, not cosmetics — `TestWithIsolatedPool_PassesThroughNonInjector`
now checks the real shared fake instead of a fake made for this one test,
so if someone adds `Fake.WithHTTP`, the test will break loudly.
`recordingInjector`/`recordingClient` **stay**: they exist in order to
implement `HTTPInjector` and remember the injected HTTP clients, which
`Fake` by definition does not do.

#### What 7C did

Closing out the stage: one design change and two removals of dead code, each
in a separate commit. Zero changes in the goldens.

- **`Conductor` rejects a parallel `Submit`.** The contract stopped being
  a comment. A second, parallel `Submit` gets an explicit `ErrTurnInFlight`
  (a sentinel next to `ErrNoResolver`). Rejected variants and why they
  fell out: a **mutex** would silently serialize the calls, so a frontend bug
  would look like a hang instead of like an error; a **queue** is a separate feature
  nobody ordered, with its own questions about ordering and cancellation.
  What is decisive is **where** the check stands: `Submit` used to start by
  appending the user message to the conversation and writing it to the session log,
  so a rejection made anywhere later would leave a trace of
  a call that "did not go through". Claiming the turn is atomic (a single
  `test-and-set` in a single critical section under the existing `c.mu`)
  and happens **before the first state mutation**; the flag is cleared
  in a `defer` registered immediately, so neither an early `return` nor
  a panic in the agent loop will leave the conductor busy forever.
  The scope is deliberately narrow: `Submit` is protected against a second `Submit`.
  `Messages()`, `Usage()`, `SetHistory()` and the rest **still** belong to the
  goroutine running the conversation — scattering mutexes over the getters
  is a different, bigger design change and was not done. `Interrupt` unchanged.
  The test `TestConductor_ConcurrentSubmitIsRejected` (under `-race`) proves
  three things: exactly one `Submit` goes through, the rejected one **left no
  trace** in the conversation, and after the first finishes the next `Submit`
  goes through again. Which of the two prompts wins is up to the scheduler
  and the test does not assume it.
- **The dead `if` in the ESC branch** (`tui_mode.go`) removed. The receive from the channel
  stayed — waiting for the agent to finish is load-bearing, because the agent still writes to
  display and the screen must not be repainted before it ends — but with no
  assignment and no condition, with a comment saying **why** the result
  is discarded (the error was already shown by `d.Error()` in `agent.Run`).
- **The dead constant `subagentDefaultMaxIterations`** (`main.go`) removed.
  The comment above it, however, described a real behavior change (the default
  value stopped being a hard-coded 10, so calls omitting
  `MaxIterations` run without a limit) — that knowledge concerns
  `tools.DefaultSubagentMaxIterations`, not the dead alias, so it was
  **moved** to the comment at the constant itself, not deleted together
  with the code that held it.
- `record.go` / `replay.go` — crossed out of the plan, rationale at the item
  itself above.

#### What stage 7 did NOT do

- **Injectable `BaseBackoff` in the retry loop** — still not done, the description of the debt
  is below in "To fix". It is a design change in production code of the
  same kind as `agent.Sink` or `providers.AuthSource` and deserves
  its own decision, not tacking onto a stage about test connectors. The cost
  we pay for it today is itemized at that item: the retry path
  for errors without a `Retry-After` header (500, EOF) remains uncovered, because
  a test would have to really sleep for four seconds.
- **`Conductor` writes to `os.Stderr`** in `session_lazy.go` — untouched,
  see "To fix".
- Two hand-written `connector.ModelClient` fakes in `main_resolve_test.go`
  (`recordingClient`, `recordingInjector`) **stay on purpose**: they implement
  `HTTPInjector`, which `Fake` by definition does not do.

### To fix — surfaced at stage 6

The first two items are literally an order for stage 7: without them `Fake` will not cover
what the local fakes cover today, and rewriting the tests would be a regression
of coverage. The rest is debt found along the way and deliberately untouched.

- [x] **`Fake` must be able to hang until cancelled, not only play back a script.**
      The fake in `conductor/conductor_test.go:39-58` has a `blockUntilCancel` mode:
      `Stream` ignores the script, blocks and reports cancellation as
      `stream.StreamError{ctx.Err()}` — just as the real connectors do.
      A naive `Fake` playing back only an event sequence does not have this,
      and without it neither `Interrupt()` nor any ESC path can be tested.
      Design it right away, do not bolt it on later.
      Done in 7A as the field `Fake.BlockUntilCancel`; the same mode serves
      `blockingProvider` from `agent/agent_test.go:1155-1172`, which disappears in 7B.
- [ ] **Backoff in the retry loop is not injectable — and that is the only reason
      the retry tests do gymnastics with context cancellation.**
      Found at 7B, deliberately untouched: making it injectable is
      a design change in production code, of the same kind as `agent.Sink`
      or `providers.AuthSource`, and deserves its own decision.
      What is missing: `agent/agent.go:147-149` computes backoff through
      `api.CalcBackoff(attempt, lastErr, api.RetryConfig{MaxRetries: cfg.MaxRetries})`
      and sleeps through `sleepWithCountdown`. `RetryConfig` has a `BaseBackoff`
      field, but the loop does not fill it, so `WithDefaults` puts in 4 — **a minimum
      of four seconds of sleep per attempt**, growing exponentially. Neither `agent.Config`
      nor `Run` has anything to change that with, and `sleepWithCountdown` calls
      `time.After` directly.
      What this costs today: `TestRun_TotalCalledOnAllRetriesExhausted`
      (`agent/agent_test.go`) runs `Run` in a goroutine and polls the display
      waiting for `ToolBlock("retry 1/5 …")` in order to cancel the context
      during the first backoff. This whole construction exists solely so as
      not to sleep. For the same reason the new mid-stream failure test
      (`TestRunFallback_MidStreamFailureAfterPartialText`) must use a **non**-retryable
      error — a retryable one would lead it into the same trap.
      The only escape hatch that exists today — and it is narrow: `CalcBackoff` honors
      the `Retry-After` header from a 429 literally, so `connectortest.RateLimited("0")`
      asks for zero sleep and `sleepWithCountdown` returns immediately. Used by
      `TestRun_RetryRecoversAfterMidStreamRateLimit` — new in 7B,
      covers a **successful** retry, which nothing covered before (the only earlier
      retry test cancels the context in the first backoff and never reaches
      recovery). The hatch does not work for 500 or EOF: there it is always four
      seconds from `BaseBackoff`, so the retry path for errors without `Retry-After`
      remains uncovered.
      What injection would give: a script "500, 500, then success" and "retries
      exhausted" without cancellation gymnastics and without waiting. To be decided:
      a `BaseBackoff` field in `agent.Config` passed to `RetryConfig`, or an injected
      `Sleep func(context.Context, time.Duration) error`.
- [x] **`Conductor` does not guard against a parallel `Submit`.** The contract "all
      methods from the goroutine running the conversation, the exception is `Interrupt`" is
      a comment (`conductor/conductor.go:89-91`), not a mechanism. No
      frontend breaks it today, but an RPC frontend — exactly what this
      separation was created for — will break it on day one. To be decided: a mutex,
      a queue, or an explicit "turn already in flight" error. A test for this is cheap and
      naturally belongs to stage 7 (`-race` + two parallel `Submit`s).
      Done in 7C as `ErrTurnInFlight` — the decision and, more importantly, the
      **place** of the check are described in "What 7C did".
- [ ] **`Conductor` writes to `os.Stderr` in one place**
      (`conductor/session_lazy.go:44`, the warning "continuing without session").
      Carried over literally, so it is not a regression, but in a package whose
      whole point is that it does not know which frontend it has, it is a jarring note.
      A candidate for an injectable hook `Warn func(error)` — exactly the way
      `providers.AuthFile` solved the same problem in stage 4.
- [x] **A dead `if` with an empty body in the ESC branch** (`tui_mode.go:277-279`):
      `if !errors.Is(res.err, context.Canceled) && res.err != nil { }` plus
      the comment "Real error, not just cancellation". The condition is computed and
      thrown away. Either the error is to be shown or the condition has to go — today
      it looks like unfinished error handling, and under `-race`/the linter nobody will catch it,
      because it is formally correct.
      Done in 7C: the condition went away, because the error IS already shown by
      `agent.Run`; only the receive from the channel remained, which is load-bearing.
- [x] **Dead constant `subagentDefaultMaxIterations`** (`main.go:130`) — an alias for
      `tools.DefaultSubagentMaxIterations`, used nowhere. The same kind of
      debt as the `agent.Config` fields removed in stage 6.
      Done in 7C, together with moving the knowledge from its comment to
      `tools/tool.go`.
- [ ] **If "free models" ever return** (cut out in stage 6), they must
      return as a property of a catalog entry (`ModelEntry`), not as a second
      method of the `Provider` interface. The previous shape rotted precisely because
      it was a method nobody had anything to populate.

---

## Notes

Risks:
- stage 2 moves message conversions — the most prone to a silent regression (hence stage 0)
- `agent/agent_test.go` (1216 lines) to be rewritten — mechanical, but voluminous.
  `providers/providers_test.go` was listed here as 844 lines: that is the number from before the
  split in stage 2, today the file has 282 lines and stage 4 did not have to rewrite it.

Gains beyond cleanliness: build tags and stub files disappear, the dead `api/client.go` disappears,
retry tests stop needing an HTTP server.

In total ~8–9 days. Stages 1, 2 and 7 give ~80% of the value — it is possible to stop before stage 6.

Out of scope (separately): the global `tools` registry and `tools.SetSubAgentRunner`.

---

## Bugs found at stage 0

The golden files freeze the current (incorrect) behavior on purpose. Each fix = a deliberate
break of the golden + regeneration with `-update`, in a SEPARATE commit — never alongside
stages 2-3. Otherwise a red test stops distinguishing "the move broke something"
from "we changed behavior".

The ordering criterion is not "before or after the refactor", but **whether the fix can be
verified**. A golden proves that something did not change — not that the new behavior
is correct. A fix requiring provider documentation and a real call is a separate
job with a different feedback cycle.

### Now — cheap, verifiable offline

- [x] **openai: multiple `toolResult`s in one message merge into one** — texts glued together without a separator, `tool_call_id` overwritten by the last block (`convert.go:68-73`). Pure logic, the correct pattern is right next to it (anthropic/gemini). Fixed before stage 1, so that stage 2 moved correct code instead of arming a trap. Note: the bug was dormant — `agent/run_tools.go:64` emits 1 message per 1 tool call, and resume reproduces 1:1.

### A separate task AFTER stage 4 — not "while at it" in any stage

Gemini: three defects scattered across `parseURI` (path), the switch in `config.go`
(model), `api/gemini.go` (header). `case "gemini": // different path structure`
is directly a symptom of a missing abstraction — the connector builds its own URL and headers.
Requires Gemini documentation + a real call with a key.

The marker wandered: stage 2 → 3 → 4. Stopped here as a **standalone task after
stage 4**, because the goldens are the safety net of the refactor: fixing the wire format
in the same stage as moving code removes the ability to tell "the move
broke something" from "we changed behavior on purpose". The `TODO` in `connector/gemini.go`
stays in place until then.

- [ ] **gemini: `role: "assistant"`** in `contents[]` — Gemini knows only `user`/`model` (`convert.go:173-176`)
- [ ] **gemini: missing path and model** — `POST /` instead of `/v1beta/models/<model>:streamGenerateContent`; `GeminiRequest` has no `model` field, so `req.Model` is lost
- [ ] **gemini/anthropic: `Authorization: Bearer`** instead of `x-goog-api-key` / `x-api-key` (`api/gemini.go:59`, `api/anthropic.go:102`) — works only through an OpenAI-style proxy

### Separately, whenever — these are design decisions, not bugfixes

They require settling a convention or touch consumers (`display/`), so they
do not belong in the vicinity of the refactor.

- [ ] **`IsError` honored only by Anthropic** — openai and gemini have no native equivalent, a convention has to be DECIDED, not "fixed"
- [ ] **`thinking` blocks dropped in all 3 converters** — relevant for Anthropic extended thinking (requires sending back signed blocks)
- [ ] **`Finish.Reason` not normalized** — `tool_calls` / `tool_use` / `STOP` / `stop` (gemini mixes letter case)
- [ ] **`ConvertToolsToAnthropic` on a parse error returns the OpenAI format as-is** and logs with the global `log.Printf` (`api/anthropic.go:362`)

---

## Pre-existing debt (not our regression, found along the way)

- [x] `go test -tags "noanthropic nogemini" ./api/` **did not compile** — `testCtx()`
  sits in `api/anthropic_test.go` (a file with `//go:build !noanthropic`), and `api/api_test.go`
  uses it. Verified on a clean tree before stage 2: the same error.
  `go build -tags ...` passes, so `make minimal` works; the problem concerned only
  running tests with tags. Fixed in stage 3 (helpers moved to an
  untagged file, gemini tests split out under `!nogemini`).
- [x] `gofmt` of the whole repo done in a separate commit (8caa1ff) — before stage 2.
- [x] **`IsConfigured()` repeats a loop-invariant lookup.** `p.authSource().Key(p.name)`
  does not depend on `e`, yet it stands in `for _, e := range p.entries` — for a provider without a key
  it executes as many times as it has models (617 for `nano-gpt`). `connect.GetKey` →
  `LoadAuth()` reads and parses `auth.json` on every call, with no cache.
  Measured on a real catalog (128 providers, 3827 models): `FindModel` on
  a missed name without a prefix = **11.8 ms** and ~3800 file reads. That is O(models)
  where O(providers) is needed. The old code had an identical loop — stage 4 only
  made the invariance visible. Fix: hoist the call out of the loop + a caching
  decorator on `AuthSource` (that is exactly the place where such a thing belongs).
  Not urgent: 11.8 ms hurts nobody, the page cache amortizes it.
  **Done:** lookup moved below the loop (`providers/config.go`,
  `dynamicProvider.IsConfigured`) — every entry with a token in the URI still
  short-circuits without any I/O (no change in that branch), and providers without a token
  read `auth.json` at most once per call, not once per model. A test with a
  spied `AuthSource` (`TestDynamicProviderIsConfigured_authSourceCalledOncePerProvider`,
  `..._uriTokenShortCircuitsBeforeAuthSource`) pins both properties and
  deliberately FAILS on the old loop (verified with a mutation check: a counter of 3
  calls instead of 1). Commit `d2cd0f2`.
  **Deliberately NOT done:** a caching decorator on `AuthSource`. After this
  fix the cost is O(providers) — ~128 reads instead of ~3800 (11.8 ms →
  ~0.4 ms) on a real catalog. A cache would bring it down to one read for the whole
  process, but at the price of a real bug: a long-lived REPL/TUI would stop seeing
  a key added by `tyci provider auth set` from a second terminal until the
  process died. 0.4 ms is not worth a staleness bug — a decision, not a
  backlog item.
- [x] **`IsConfigured` checks the URI token raw, `Stream` resolves it.** An entry
  with an unresolvable `$FOO` shows up as configured and blows up only
  on request. A pre-existing asymmetry, deliberately preserved in stage 4 (otherwise
  `provider list` would start hiding providers that the user configured) —
  to be decided as a convention, not "fixed" silently.
  **Done:** the `IsConfigured()` verdict stays EXACTLY as before
  (no existing test changed) — the asymmetry is already a convention, not
  a bug. Added a third, diagnostic channel: `Provider.ConfigWarnings() []string`
  names the environment variables referenced by the provider's URI entries and
  which are empty/unset (deduplicated, sorted —
  `dynamicProvider.ConfigWarnings`, `providers/config.go`). `provider list`
  prints them under the provider without changing `✓`/`(not configured)`
  (`commands.go`). `resolveAPIKey` names the missing variable in the error
  message when `uriKey` is an environment reference (`connect.LooksLikeEnvRef`);
  the path without a reference keeps the existing message to the character
  (`TestDynamicProvider_ResolveAPIKeyErrorMessage`, unchanged). Two
  `Provider` fakes in tests (`fakeProvider`, `catalogStub`) got a one-line
  `ConfigWarnings` returning `nil` — checked with grep that there is no fourth
  implementation. Commit `4b59f9a`.
  **Deliberately NOT done:** `ConfigWarnings` sees only references from the URI, not
  from `auth.json` — `AuthFile.Key` returns a bare string after `connect.ResolveToken`,
  so an unresolved reference written by hand into `auth.json` is, from this
  side, indistinguishable from a missing key. In practice this path requires manual
  editing of the file, because `provider auth set` (`commands.go:760`) rejects
  an unresolvable `$FOO` already at write time. Covering it would require changing the
  `AuthSource` interface, which today returns a bare `string` — out of scope of this
  fix.
