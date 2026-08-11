## Status (2026-08-11)

- **Markdown blank-lines**: naprawione (`collapseMarkdownBlankLines`,
  commit `b266597`, podpięte w `display/tui_markdown.go:56`).
- **Lock P1** (locki znikają po ~60s mimo braku `seconds`): naprawione
  (`agent/tools_exec.go`, commit `52d3cf7`) — `lock`/`unlock` dostały
  `toolTimeout = 0`, tak jak wcześniej `subagent`/`wait`.
- **Lock P2** (`write`/`edit` nie sprawdzają `lockRegistry`): to znany,
  świadomy brak zakresu z pierwotnego planu (`locks/` to advisory
  locking — patrz `docs/subagent-testing-plan.md`), nie regresja. Do
  zrobienia jako osobny krok integracyjny, jeśli ma być egzekwowane
  fizycznie.
- **Lock P3/P4** (read ostrzega o locku, koordynacja między agentami
  przez `note`+eventbus): propozycje, jeszcze nie zaakceptowane/nie
  zaimplementowane.

# Markdown rendering — findings

Temat: w TUI odpowiedzi LLM w formacie markdown "nie do końca dobrze się ładują" — tam gdzie powinien być tekst użytkownik widzi puste linie (np. 10 pustych linii).

Poniżej zebrane obserwacje z inspekcji kodu `display/tui_*.go` i `session/resume_list.go`.

## Pipeline renderowania markdown — jak to działa

- Przychodzące delty LLM trafiają przez `TUI.Text()` → `flushLoop` → `tuiMsgBlock{kind:"text"}` → `handleBlockMsg` → `appendOrAppend("text", chunk)`.
- Podczas streamingu blok jest `dirty=true`. Render idzie przez `streamWrap` (przyrostowy wrap ostatniej linii logicznej).
- Na `done` / `tool-start` / `error` / `block` / nowy typ bloku wywoływane jest `forceRenderDirtyBlocks` — glamour renderuje całość, wynik trafia do `mdCacheRendered[idx]`, `cachedLines = strings.Split(rendered, "\n")`, `cachedLineCount = lineCount(rendered)`.
- Wyświetlanie: `renderFrame` → `buildMessageRegion` → `buildFlatRenderLines` → `getBlockLines` → zwraca `cachedLines`.

## Co zauważyłem (oczywiste)

1. **Glamour wstawia pionowe odstępy między sekcjami markdown.**
   Dla typowej odpowiedzi z 3 nagłówkami, listą i zakończeniem dostajemy
   ~9 linii z treścią i ~6 pustych "padding" linii (każda o `lipgloss.Width = width`,
   bo glamour wypełnia każdą linię do `m.width`). Po strimowaniu ANSI każda
   taka "padding" linia to po prostu spacje — wygląda jak pusty wiersz
   w terminalu. To zachowanie glamoura, nie naszego kodu.

2. **Różnica `lineCount` vs `len(strings.Split(...))`.**
   `lineCount(s)` = `newlines + 1` (1 nawet dla `""`). `len(strings.Split("", "\n"))` = 1.
   Spójne, ale `forceRenderDirtyBlocks` używa `lineCount` a potem robi `Split`.
   Trzeba uważać, żeby `cachedLineCount` zgadzało się z `len(cachedLines)`.

3. **`renderMarkdownWithCache` zwraca `""` → brak aktualizacji cache.**
   Jeśli glamour zwróci pusty output (np. dla contentu z samych whitespace),
   w `forceRenderDirtyBlocks` nie aktualizujemy `cachedLines` ani
   `cachedLineCount`. Zostają stare wartości ze streamingu. Dla tekstu
   raczej nie zachodzi, ale jest ścieżka do potencjalnego rozsynchronizowania.

4. **`tryRenderMarkdown` trzy gałęzie.**
   - `!dirty && hasCached && cached != ""` → zwraca cache.
   - `dirty && isStreaming` → streamWrap (zwraca wrap + ustawia cached).
   - else → glamour render.
     Tu NIE ma ścieżki gdzie `!dirty && cached == ""` — wtedy leci do glamour.
     Ale jest też scenariusz `!dirty && cached != ""` ze statusem `isStreaming=true`
     (np. po forceRenderDirty, gdy nowa delata przyjdzie): zwraca cache — OK.

5. **`streamWrap.lastLen == len(content)` skrót.**
   Działa tylko gdy content nie rośnie (w streamingu rośnie zawsze). Bezpieczne.

6. **`appendOrAppend` gorąca ścieżka strumieniowania.**
   ```go
   last.cachedLineCount = 0
   last.cachedLines = nil
   ```
   Czyści oba. Potem stara się odświeżyć przez `getBlockLines`.
   Jeśli `m.cachedTotalLines >= 0`, robi to. Jeśli `< 0`, nie robi
   (zostawia nil). Wtedy `cachedTotalLines = -1` wymusi recompute
   przy następnym dostępie. Spójne.

7. **Spacer między blokami.** `buildAllFlatRenderLines` dodaje 1 pustą linię
   (spacer) między każdą parą bloków (`kind != "tool"` sąsiedzi).
   Glamour-rendered blok sam już może mieć puste linie na końcu (zwykle
   Trim je usuwa, ale jeśli ostatnia linia ma padding whitespace, zostaje).
   Razem mogą dać podwójne puste wiersze.

8. **`m.width` jest przekazywane do `renderMarkdownWithCache` jako `width`**.
   Renderer cache'uje per `maxW`. Po resize `invalidateAllBlockLineCounts`
   czyści `mdCacheRendered`. Ale sam `rendererCache` (glamour TermRenderer)
   NIE jest czyszczony — zostaje słabo używany, ale poprawnie działa
   dla nowego `maxW`. OK.

## Co się dzieje przy responsie długim

Eksperyment (test w `display/md_blankcount_test.go`, już usunięty):
```
content = "Sure!... ## Section 1\n\nLorem...\n\n## Section 2\n\nL2...\n\n- item 1\n- item 2\n- item 3\n\nEnd."
→ 15 łącznie linii, 9 z treścią, 6 pustych (separatorów markdown)
```

Czyli typowo ~40% wierszy to puste separatory glamour. Przy 3–5
nagłówkach i listach user widzi realnie tyle pustych linii ile vidzi
treści. Dla user'a "gdzie jest tekst, tam są puste linie" — bo on
liczy separator-blank-row jako "pustą linię".

## Hipotezy

A) **Glamour separators są uciążliwe.** User faktycznie widzi puste
   separatory między `##` headings i akapitami. Proponowany fix:
   w renderze strimować trailing whitespace z każdej linii glamour
   (tak żeby padding ANSI nie wypychał linii do pełnej szerokości),
   a może nawet collapse'ować 3+ blank → 1 blank.

B) **Może być real bug z line-count mismatch po force-render.**
   Jeśli glamour-render zwraca mniej linii niż streaming-wrap
   (np. URL z `_` rozbity znak-po-znaku przez streamWrap, a glamour
   łamie po kropkach), to po force-render `cachedLineCount` maleje,
   `cachedTotalLines` jest invalidowane -1. Powinno się recomputować
   poprawnie przy wyświetlaniu. Wtedy nie powinno być "pustych"
   linii — wiersze są albo treścią, albo separator-blank-row. Ale jeśli
   `cachedTotalLines` się rozjedzie z faktyczną sumą (np. przez corner case
   z `rendered == ""`), mogą być dodatkowe puste wiersze wyświetlane.

C) **Glamour failure nie obsłużony.** Jeśli glamour rzuci error,
   `renderMarkdownWithCache` pada do `wrapRawText` fallback. OK. Ale
   to może dawać inne wyniki niż streaming-wrap.

# Lock tool — findings (sekcja 3 test planu)

Temat: scenariusze z `docs/subagent-testing-plan.md` sekcja 3 (lock/unlock).
Manualne odpalenie w runtime tyci-agent ujawniło dwa problemy w warstwie
integracji — logika w `locks/registry.go` i `tools/lock.go` jest poprawna
(testy jednostkowe zielone), ale wire-up do reszty narzędzi nie domyka
kontraktu "posiadanie locka blokuje edycję".

## Co mówi scenariusz i co mówi implementacja

- Scenariusz **3.2** wymaga: drugi `lock(path)` w runtime → błąd
  "already locked by …".
- Scenariusz **3.3/3.4** wymaga: `unlock` z poprawnym holderem → sukces,
  z obcym → błąd.
- Scenariusz **3.6** implikuje: posiadanie locka powinno realnie chronić
  plik przed `write`/`edit` z innego call'a, bo inaczej lock jest
  deklaratywny i bezużyteczny w multi-agent scenariuszu.

Implementacja:

- `tools/tool.go:415` — package-level `var lockRegistry = locks.NewRegistry()`.
- `tools/tool.go:428-429` — Registry podpięte do `LockTool`/`UnlockTool`.
- `tools/write.go:13` — `type WriteTool struct{}` — **brak pola Registry**,
  `Run` nie konsultuje `lockRegistry` w żadnej gałęzi
  (`runWriteMode`, `runEditMode`, `appendFile` wewnętrznie).
- `tools/edit_write_test.go` — testy `WriteTool` nie sprawdzają locks
  (brak cross-tool integracji).
- `locks/registry.go` (`Acquire` linia ~80) — logika śledzenia holdera
  i TTL jest poprawna i pokryta testami.

## Co zaobserwowałem w runtime (manual, 2026-08-11)

```
1. lock("test/scenario-3/longttl.go") → sukces, holder H1
2. lock("test/scenario-3/longttl.go") (inny holder, ten sam path,
   bez wait/unlock między) → sukces, holder H2  ← oczekiwany konflikt
3. unlock("test/scenario-3/longttl.go", H2) →
   "could not unlock …: not locked, already expired, or held by a
    different holder"  ← H2 nie jest znany jako holder mimo "sukcesu"
4. lock("test/scenario-3/lock-then-write.go") → sukces, holder H3
5. write("test/scenario-3/lock-then-write.go", "package x\n") → OK, zapisuje
6. edit ("lock-then-write.go", old="package x", new="package edited")
   → "replaced 1 occurrence(s) … at line 1"  ← przeszło mimo locka
```

Ścieżka `/Users/piotr.halas/work/tyci-agent/test/scenario-3/*` po teście
usunięta (`rm` + `rmdir`), żeby nie śmiecić repo.

Dodatkowo: `go test -race ./tools -run "TestLockTool|TestUnlockTool" -v`
→ **11/11 PASS** — logika samego rejestru i interakcji lock↔unlock jest zielona.

## Wnioski — dwa niezależne bugi integracji

### P1 — Lock tool nie współdzieli stanu między wywołaniami w runtime

**Objaw**: kolejne `lock(path)` w tej samej sesji nie widzą siebie nawzajem;
`unlock` z holderem zwróconym przez chwilę wcześniejszy `lock` zwraca
"not locked".

**Prawdopodobna przyczyna**: package-level `lockRegistry` w `tools/tool.go`
**jest** package-level, więc powinien być shared między tool-callami w tym
samym procesie. Więc winowajca jest raczej w wyższej warstwie — albo:

- `agent/run_once.go` / `cmd_interactive.go` przekazuje tool-call jako
  osobny, krótko-żyjący `ctx`, którego `Done()` odpala cleanup goroutine
  z `registry.Acquire` (patrz linia ~95 w `locks/registry.go`:
  `go func() { ... ctx.Done() }`).
- albo każdy wrapper toolowy ma własną instancję `LockTool{...}` z
  package-level — ale to dałoby ten sam `lockRegistry` wskaźnik.

Pierwsza hipoteza pasuje do zaobserwowanego: lock jest natychmiast
usuwany po powrocie tool-call'a bo `ctx` się kończy. Wtedy nawet
`seconds:120` nie chroni, bo expiry jest liczone od acquire, a po
zwrocie `Registry.Acquire` listener na `ctx.Done()` usuwa wpis natychmiast.

Sprawdzić: w `cmd_interactive.go` i/lub `agent/run_*.go` jak `Run(ctx, …)`
jest wołane dla narzędzi — jaki `ctx`? Czy to `context.Background()`, czy
`context.WithTimeout(...)`, czy parent ctx który ginie po każdym tool-call?

### P2 — `write` / `edit` w ogóle nie konsultują lockRegistry

**Objaw**: `write` i `edit` na zablokowanej ścieżce przechodzą bez
ostrzeżenia. Lock advisory ma zerowy wpływ na ten tool.

**Przyczyna i fix** (proponowany):

```go
type WriteTool struct {
    Registry *locks.Registry   // ← dodać
}

func (t *WriteTool) Run(ctx context.Context, input map[string]any) ToolResult {
    ...
    path, _ := input["path"].(string)
    if t.Registry != nil {
        if h := t.Registry.Holder(path); h != "" && h != selfHolder(ctx) {
            return ToolResult{
                Type:    "result",
                Success: false,
                Error:   fmt.Sprintf("path %q locked by %q; unlock first", path, h),
            }
        }
    }
    ...
}
```

Ale to rozwiązuje P2 tylko **jeśli Registry jest współdzielony** —
czyli po fixie P1. Bez fixu P1, P2 nic nie zmieni (registry będzie puste).

### Zależność i kolejność naprawy

Naprawić **P1 najpierw**, potem P2. Inaczej P2 zachowa się identycznie
(unit test z mock Registry przejdzie, ale runtime nic nie blokuje).

## Hipotezy alternatywne

D) **Świadoma decyzja projektowa** — lock advisory jest deklaratywny,
   intencja jest żeby caller sam sprawdzał przed write. Ale wtedy
   scenariusz 3.2 (konflikt w lock) **nie ma sensu**, bo lock nie jest
   "czekaniem na konflikt", tylko "notatką że tu będę pracował".
   Dokument `subagent-testing-plan.md` 3.2 wyraźnie mówi o błędzie
   konfliktu — więc to NIE jest świadoma decyzja; to bug.

E) **Race w init var** — ale package-level `var = locks.NewRegistry()`
  jest thread-safe (wykonywane raz przy starcie procesu) i `mu sync.Mutex`
  w `Registry` chroni mapę. Nie wyjaśnia obserwacji.

F) **Za-ogólne fan-out** w sub-tasks testu 3.2: gdy `tid-1` uruchamia
   subagenta, ten tworzy nowy tool-runner ze świeżym portem Registry.
   Ale w moim teście **nie było subagenta** — były dwa `lock` calls w
   jednej sesji, sekwencyjnie. To wyklucza fan-out.

## Decyzja

Wracam z pytaniem: czy to jest P1 izolowane (ctx cleanup nie jest winny,
a winowajca jest inny), czy rzeczywiście winowajcą jest ctx-wrap w
`agent/run_once.go`. Diagnoza P1 wymaga:

- jednego `lock` w runtime, potem `go test` z wyciągnięciem stanu
  registry przez debug endpoint, albo
- krótkiego `log.Println` w `locks/registry.go` na acquire/release
  i śledzenie kiedy wpis wychodzi z mapy.

Szybki plan naprawczy dla obu problemów (po akceptacji):

1. `tools/lock_test.go`: dodać test integracji lock→write że write rzuca
   gdy holder inny niż self.
2. `tools/write.go`: dodać pole `Registry`, sprawdzenie w `Run` +
   w `runWriteMode` i `runEditMode` (tryb append też, bo to też jest
   modyfikacja).
3. Diagnoza P1: zlokalizować kto woła `toolRun(ctx, …)` i jaki ctx.
   Fix: albo użyć `context.Background()` dla tool-run jeśli jest
   przejściowy, albo wydłużyć ctx-bound tak żeby lock przeżył.

Po fixach odpalić: `go test -race ./...`, plus scenariusze 3.1–3.6 z
`docs/subagent-testing-plan.md` ręcznie w TUI.

## Pierwszorzędne uzupełnienie testu 3.2 w formie kodu

Gdyby lock działał poprawnie, do zaakceptowania `TestLockRuntimeIsolation`
w `tools/lock_test.go`:

```go
func TestLockRuntimeIsolation(t *testing.T) {
    lt := &LockTool{Registry: locks.NewRegistry()}

    r1, _ := lt.Run(context.Background(), map[string]any{"path": "X"})
    if !r1.Success {
        t.Fatalf("first lock failed: %v", r1.Error)
    }

    r2, _ := lt.Run(context.Background(), map[string]any{"path": "X"})
    if r2.Success {
        t.Fatalf("second lock should conflict; got success: %s", r2.Content)
    }
}
```

(Implementacja ta już przechodzi w unit test z jednym `Registry` —
problem jest wyłącznie w tym, **jak** runtime tyci-agent woła te narzędzia.)

## Decyzja

Wracam z userem i pytam o repro:
- czy dzieje się zawsze czy tylko dla konkretnego typu odpowiedzi
  (dużo nagłówków, list, code blocks, tabele)?
- czy dzieje się po resize terminala?
- czy po scroll-up do tyłu też widzi puste linie?
- czy user używa myszy do selekcji tekstu (bo `renderSelectableLine`
  może w specyficznych warunkach zjadać treść)?

# Lock — rozszerzenia (P3, P4)

Temat: po P1+P2, lock advisory w runtime powinien też (a) informować
czytającego agenta że plik jest w trakcie edycji gdzie indziej, i
(b) umożliwiać koordynację między-agentową tak żeby agent mógł
"poczekać" aż inny skończy zamiast iść na ślepo.

## P3 — `read` informuje o locku (read-aware, nie blokuje)

### Decyzja projektowa: read zwraca **warning**, nie błąd

`read` nie mutuje pliku. Twarde zablokowanie czytaącego agenta
miałoby efekt uboczny: agent musiałby najpierw wziąć lock dla siebie
na czas jednego `read` — co psuje composability i jest zbyt sztywne
dla narzędzia które z definicji nie zmienia stanu. Zamiast tego `read`
powinien **wstrzyknąć do wyniku** jednoznaczny nagłówek ostrzegawczy
i nic poza tym.

### Kontrakt

Wejście: `read(path: "foo/bar.go")`, gdzie ścieżka jest zablokowana
przez inny holder Hx (ważne: jeśli Hx to ja sam, brak warningu).

Output: istniejący `ToolResult.Content` z prependem:

```
[NOTE: foo/bar.go is currently locked by holder "holder-6e76d17fa51a"
since 2026-08-11T14:21:09Z (expires 2026-08-11T14:26:09Z, 4m left).
File contents mirror that point in time and may not reflect the
holder's final state. To wait for completion, run
`wait(job_id: "...")` or call `lock(path: "foo/bar.go", seconds: N)`
yourself, then re-read after unlock.]


<existing read output here, unchanged>
```

Kryteria:
- `Success` dalej `true` (model nie ma być zmuszony do retry/error path).
- Treść pliku zwracana bez zmian — read czyta to co jest na dysku,
  ostrzeżenie to meta nad treścią.
- Brak `NOTE` jeśli `Registry == nil` albo brak locku albo holder
  to `selfHolder(ctx)`.

### Implementacja szkielet (proponowana)

```go
type ReadTool struct {
    Registry *locks.Registry   // opcjonalny
    // SelfHolder, jeśli nie ustawiony, używa domyślnego "holder-<token>"
    // z parent kontekstu sesji (identity modelu w parent turn). Caller
    // nie musi go podawać jeśli Registry jest shared.
}

func (t *ReadTool) Run(ctx context.Context, input map[string]any) ToolResult {
    path, _ := input["path"].(string)
    ...

    if t.Registry != nil {
        if h, since, expires := t.Registry.HolderWithMeta(path); h != "" && h != t.Registry.SelfHolder(ctx) {
            expiry := "no expiry"
            if !expires.IsZero() {
                ttl := time.Until(expires).Round(time.Second)
                expiry = fmt.Sprintf("expires %s (%s left)", expires.Format(time.RFC3339), ttl)
            }
            content = fmt.Sprintf(
                "[NOTE: %s is currently locked by holder %q since %s (%s).\n"+
                "File contents reflect that point in time and may not "+
                "reflect the holder's final state. To wait, call "+
                "`lock(path:%q, seconds:N)` for yourself, then re-read "+
                "after that holder unlocks.]\n\n%s",
                path, h, since.Format(time.RFC3339), expiry, path, content)
        }
    }
    ...
}
```

`Registry.HolderWithMeta(path)` to nowa metoda na `locks.Registry`,
zadeklarowana obok `IsLocked`/`Acquire`/`Release`, zwracająca holder,
`AcquiredAt`, `ExpiresAt` atomowo pod `r.mu`.

### Testy (`tools/read_test.go`)

Nowe:

- `TestReadNoteOnLockByOther`: Registry z lockiem przez H-other →
  output zaczyna się od `[NOTE: ...]`, treść pliku nienaruszona,
  `Success == true`.
- `TestReadNoNoteOnLockBySelf`: Registry z lockiem przez H-self
  (SelfHolder == acquired) → brak NOTE.
- `TestReadNoNoteWhenNoLock`: Registry pusty → brak NOTE.
- `TestReadNoNoteWhenNilRegistry`: brak wiring → brak NOTE
  (regresja — nie wolno zepsuć istniejącego ścieżki).

### Komunikat — checklist

Żeby ostrzeżenie było realnie użyteczne dla modelu (nie dla ludzi):

1. **Kto**: pełny holder id (`holder-…`). Bez niego agent nie wie na
   kogo ma czekać ani jak go zwolnić (do tego trzeba osobnego
   subagenta — patrz P4).
2. **Od kiedy**: ISO timestamp (`RFC3339`). Pomaga ocenić czy lock
   jest świeży (ktoś mógł się wywalić 5 minut temu) czy stary
   (raczej aktywny).
3. **Do kiedy**: albo "expires …" (gdy lock ma TTL), albo "until you
   or they call `unlock`" (bez TTL). Jasne rozróżnienie, bo reguły
   wycofania są różne.
4. **Co robić**: sugestia następnej akcji (`lock` sam, potem
   re-read; albo `wait` na job_id jeśli jest). Komunikat musi
   **podać komendy do wklejenia**, nie ogólnikowe "wait for them".

Bez tych 4 punktów warning jest ignorowany — model widzi blok
tekstu i traktuje go jak treść pliku.

### Scenariusze do dopisania w `docs/subagent-testing-plan.md`

- **3.7**: `lock(path:"X")` jako agent A, potem `read("X")` jako
  agent B (albo ten sam w kolejnej turze) → NOTE w treści, holder
  widoczny, sugestia komendy. Treść pliku nienaruszona.
- **3.8**: jak 3.7, ale z `seconds:5` na locku A → NOTE ma
  `expires … (X left)` i poprawnie maleje co `wait`.
- **3.9**: lock własny → read bez NOTE.

## P4 — koordynacja między agentami: "poczekaj na lock"

### Problem

Agent A bierze `lock(path:"X")` i zaczyna edycję. Agent B w tym
samym procesie potrzebuje czytać i/lub edytować `X` — bez
koordynacji B albo pisze na ślepo (stary read → stary kontekst),
albo bierze drugi lock (3.2 blokuje — ale **użytecznie** dopiero
po P1+P2+P3).

Potrzebna jest:

1. Atomowa znałość "kto trzyma" **niezależnie** od `lockHolder`
   — bo lock holder to losowy hex (nie kto). Trzeba powiązać
   holder z akcją która go wystawiła (zazwyczaj: konkretny
   subagent z jego modelem/agent name/job_id).
2. Mechanizm "poczekaj aż X się zwolni" — naturalnie implementowany
   jako wait na `job_id`, bo zwolnienie locka przez innego agenta
   to efekt uboczny jego joba.

### Proponowany kontrakt

**Krok A**: `lock` tool z nowym polem `note` (string opcjonalny):

```
lock(path: "internal/foo.go", note: "implementer: rewriting parser")
```

Holder zwracany dalej jest losowy (np. `holder-6840ffbb75e0`) — to
jest nasz słownikowy identyfikator techniczny. **Ale** registry
przechowuje teraz pary `(holder → note)`. `read` (P3) i `lock` (3.2)
pokazują note w komunikacie błędu/ostrzeżenia, np.:

```
path "foo/bar.go" already locked by "holder-95f952e9056b"
  since 2026-08-11T14:21:09Z (note: "implementer: rewriting parser",
  expires 2026-08-11T14:26:09Z)
```

**Krok B**: subagent tool w trybie async zwraca `job_id`, którego
koniec korelujemy ze zwolnieniem wszystkich locków wystawionych przez
tę subagent-run. Realizacja:

- `tools/subagent.go` `runAsync` (po register w job registry) —
  przy zakończeniu joba A, znajdź wszystkie `(path, holder)`
  wystawione w jego lifetime i zwolnij te których TTL=0 (semafor
  sesyjny). Jeśli user ustawił `seconds`, zostaw — to expiry sam
  zdecyduje.

Tym sposobem inny agent może:

```
wait(job_id: "job-1234-…", seconds: 60)
# albo:
lock(path: "foo/bar.go", note: "implementer: rewriting parser")
```

i **wiedzieć** kiedy skończyć czekać.

**Krok C** (opcjonalny): subagent `request_lock(path, note, from)`.
Wraca natychmiast albo z `lock_id` jeśli wzięło, albo z
`{ ok: false, held_by: "…", note: "…" }` jeśli nie. Nie blokujemy
turna — agent rodzic może zaakceptować albo spróbować innej ścieżki.

### Komunikacja między agentami — obieg

Przepływ dla dwóch równoległych implementerów edytujących
ten sam plik:

```
A: lock(path:"x.go", note:"implements parseFoo")
   # holder-X, registry entry (X, note) created
A: pisze poprawki (write/edit po kolei)
B: read(path:"x.go")
   # output z NOTE: "...locked by holder-X (note: implements parseFoo)..."
B: decyduje:
   (a) wait na job_id A → bo A ma lock,
       A skończy się → registry cleanup → B widzi empty,
       B czyta ponownie czysty wynik.
   (b) albo czeka w Model-turn na notyfikację eventbus (jeszcze nie ma)
       i dostaje monit z treścią "A released lock on x.go".
```

### Wymagania implementacyjne P4

| Co | Gdzie | Skala |
|----|--------|-------|
| Pole `note` w `lock`/`Registry.Acquire` | `tools/lock.go`, `locks/registry.go` | trywialne |
| Lock output/cache pre-existing `second` | OK | istniejące |
| Subagent runner tracking locks w swoim lifetime | `tools/subagent.go`, `agent/run_once.go` | średnie |
| Job completion → cleanup locks | ten sam runner | średnie |
| Eventbus sygnał "lock released" do rodzica | `eventbus/` | nowe |
| Nowy tool `request_lock` | `tools/lock.go` (rozszerzenie) | małe |
| Testy integracyjne 3.7–3.9 (z P3) | `tools/lock_test.go`, `tools/read_test.go` | małe |

### Scenariusze do dopisania

- **4.9 / P4.A**: subagent A bierze lock z `note`, subagent B dostaje
  read z NOTE zwierającym `note` A.
- **4.10 / P4.B**: po zakończeniu A jego lock czyszczony auto; B
  otrzymuje eventbus通知 ("path X unlocked by holder …").
- **4.11 / P4.C**: A nie oddaje locka w oczekiwanym czasie → B dostaje
  timeout w `wait(job_id:A, seconds:N)` z komunikatem "wait cancelled
  while A still holds X".

## Decyzja

Wracam z userem i pytam:
- czy dla P3 read **ma być warningiem** (czytamy dalej, tylko info),
  czy twardym "odmów" (model musi najpierw lock sam)?
  Rekomendacja: warning — read nie mutuje.
- czy dla P4 `note` wystarczy jako string, czy potrzebujemy strukturalnie
  `{intent, agent, job_id}` tak żeby inny agent mógł automatycznie
  czekać na `job_id`? Rekomendacja: strukturalnie + auto-wait w `wait`
  akceptującym też `lock_id`.
