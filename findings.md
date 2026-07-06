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

## Decyzja

Wracam z userem i pytam o repro:
- czy dzieje się zawsze czy tylko dla konkretnego typu odpowiedzi
  (dużo nagłówków, list, code blocks, tabele)?
- czy dzieje się po resize terminala?
- czy po scroll-up do tyłu też widzi puste linie?
- czy user używa myszy do selekcji tekstu (bo `renderSelectableLine`
  może w specyficznych warunkach zjadać treść)?
