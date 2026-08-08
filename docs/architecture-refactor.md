# Refactor: wymienny frontend / agent / provider / connector

Cel: każdy element wymienny i testowalny osobno.

```
frontend  (tui | console | minimal | headless | rpc)
    │  Submit(prompt), Interrupt();  odbiera eventy przez Sink
    ▼
agent     (pętla, tools, sesja, retry, fallback)
    │  widzi WYŁĄCZNIE: ModelClient + Sink
    ▼
provider  (uniwersalny: katalog modeli, URI, auth, wybór connectora)
    ▼
connector  openai │ anthropic │ gemini │ responses ║ fake │ replay │ flaky
    ▼
HTTPDoer  (wstrzykiwany per connector)
```

Kluczowe: `Provider` przestaje być interfejsem, staje się jedną strukturą.
Wymienny jest connector, nie provider.

## Stan wyjściowy

Gotowe:
- `display.Display` — interfejs, 4 implementacje (TUI/Terminal/Minimal/collector)
- `providers.Provider` — interfejs, jest `mockProvider` w testach agenta
- `api.ClientFromContext` — podmiana `*http.Client`, testy na `httptest`

Do naprawy:
- `agent` importuje `display` (`agent.go:12`, `run_once.go:10`, `fallback.go`)
- brak connectorów — jest `switch` w `providers/config.go:250-330`
- `api.StreamChat/StreamAnthropic/StreamGemini` to funkcje pakietowe → build tagi `noanthropic`/`nogemini` jako obejście
- `api/client.go` — martwy, równoległy model danych
- globalne singletony: `providers.providers`, rejestr tools, `SetSubAgentRunner`
- `agent/fallback.go:36` woła globalny `providers.FindModel`
- własne `&http.Client{}` w `internal/mcp`, `internal/connect`

---

## Etap 0 — siatka bezpieczeństwa (0,5d) — ZROBIONE

- [x] `go test ./... -count=1` zielone przed startem
- [x] testy charakteryzujące `dynamicProvider.Stream` dla 3 apiType przez `httptest`
- [x] golden files z wysyłanym JSON-em (ochrona wire formatu)

`providers/wire_golden_test.go` + `providers/testdata/wire_{openai,anthropic,gemini}_{request,events}.golden.json`.
Golden zamraża metodę, ścieżkę, whitelistę nagłówków i ciało; drugi golden — sekwencję `stream.Event`.
Regeneracja: `go test ./providers/ -run TestWireGolden -update`.
Plik ma `//go:build !noanthropic && !nogemini` — do skasowania w etapie 3 razem ze stubami.

## Etap 1 — odwrócenie `agent → display` (0,5d)

- [ ] `agent.Sink` = kopia dzisiejszego `display.Display`
- [ ] sygnatury w `agent.go` / `run_once.go` / `fallback.go` na `Sink`
- [ ] zero zmian w `display/` (typowanie strukturalne załatwia sprawę)
- [ ] weryfikacja: `go list -deps ./agent | grep display` → pusto

## Etap 2 — pakiet `connector` (1,5–2d)

- [ ] `connector/connector.go`: `Connector`, `Endpoint`, `Factory`, `Registry` (wartość, nie global)
- [ ] `connector/openai.go` + przeniesienie `RichMessagesToChat`
- [ ] `connector/anthropic.go` + `RichMessagesToAnthropic`, `ConvertToolsToAnthropic`
- [ ] `connector/gemini.go` + `RichMessagesToGemini`, `convertToolsToGemini`
- [ ] ciała connectorów najpierw tylko wołają dzisiejsze `api.StreamX` (bez zmiany HTTP)
- [ ] `dynamicProvider.Stream` skrócone do: URI → klucz → `registry.New` → `conn.Stream`
- [ ] golden files z etapu 0 nadal przechodzą

## Etap 3 — `HTTPDoer` (1d)

- [ ] `type HTTPDoer interface{ Do(*http.Request) (*http.Response, error) }`
- [ ] `api.StreamX(...)` → metody na strukturze z polem `HTTPDoer`
- [ ] `ClientFromContext` jako fallback gdy `Endpoint.HTTP == nil`, potem usunąć
- [ ] to samo pole w `internal/mcp/http.go`, `internal/connect/{connect,modelsdev}.go`
- [ ] usunąć martwy `api/client.go`
- [ ] usunąć build tagi + `api/anthropic_stub.go`, `api/gemini_stub.go`
- [ ] `make minimal` = nierejestrowanie connectorów (jedna linia)

## Etap 4 — provider jako struktura (1d)

- [ ] `providers.Provider` interface → struct (`catalog`, `auth`, `connectors`, `http`)
- [ ] `AuthSource` jako interfejs (auth.json / env / literal)
- [ ] `providers.Default` zostaje dla CLI; testy budują własny katalog
- [ ] przepisać `providers/providers_test.go` (844 linii)

## Etap 5 — fallback poza agentem (0,5d)

- [ ] `Config.FallbackModels []string` → `[]ModelClient` rozwiązane przez wywołującego
- [ ] `agent/fallback.go` przestaje wołać `providers.FindModel`
- [ ] `providers.WithProvider` / `ProviderFromContext` → `ModelClient` w kontekście (`main.go:35`, `run_once.go:16`)
- [ ] weryfikacja: `agent` nie importuje `providers`

## Etap 6 — frontend jako sterownik (2d)

- [ ] `Conductor`: `conversation` + `cfg` + `ModelClient` + sesja
- [ ] API: `Submit(prompt)`, `Interrupt()`, `SwitchModel()`
- [ ] przeniesienie logiki z `interactive_agent.go`, `tui_mode.go:19-350`, `commands.go:86-330`
- [ ] TUI/console tylko wołają metody i renderują eventy
- [ ] smoke test: headless driver bez żadnego UI

## Etap 7 — connectory testowe (1d)

- [ ] `connector/connectortest/fake.go` — skryptowana sekwencja `stream.Event`
- [ ] `flaky.go` — dekorator wstrzykujący 429 / 500 / EOF w środku streamu
- [ ] `record.go` / `replay.go` — nagrywanie do JSONL, odtwarzanie w CI bez klucza API
- [ ] przepisać testy retry/fallback z `httptest` na `Flaky`
- [ ] zastąpić `mockProvider` z `agent/agent_test.go` przez `Fake`

---

## Uwagi

Ryzyka:
- etap 2 przenosi konwersje wiadomości — najbardziej podatne na cichy regres (stąd etap 0)
- `agent/agent_test.go` (1216 linii) i `providers/providers_test.go` (844) do przepisania — mechaniczne, ale objętościowe

Zyski poza czystością: znikają build tagi i pliki stubów, znika martwy `api/client.go`,
testy retry przestają potrzebować serwera HTTP.

Łącznie ~8–9 dni. Etapy 1, 2 i 7 dają ~80% wartości — można zatrzymać się przed etapem 6.

Poza zakresem (osobno): globalny rejestr `tools` i `tools.SetSubAgentRunner`.

---

## Bugi znalezione przy etapie 0

Golden files zamrażają obecne (błędne) zachowanie celowo. Każda naprawa = świadome
pęknięcie golden + regeneracja z `-update`, w OSOBNYM commicie — nigdy przy okazji
etapów 2-3. Inaczej czerwony test przestaje odróżniać „przeniesienie coś zepsuło"
od „zmieniliśmy zachowanie".

Kryterium kolejności to nie „przed czy po refactorze", tylko **czy naprawę da się
zweryfikować**. Golden dowodzi, że coś się nie zmieniło — nie że nowe zachowanie
jest poprawne. Fix wymagający dokumentacji dostawcy i realnego wywołania to osobna
robota z innym cyklem sprzężenia zwrotnego.

### Teraz — tanie, weryfikowalne offline

- [x] **openai: wiele `toolResult` w jednej wiadomości zlewa się w jedną** — teksty sklejone bez separatora, `tool_call_id` nadpisany przez ostatni blok (`convert.go:68-73`). Czysta logika, wzorzec poprawny obok (anthropic/gemini). Naprawione przed etapem 1, żeby etap 2 przenosił poprawny kod zamiast uzbrajać pułapkę. Uwaga: bug był uśpiony — `agent/run_tools.go:64` emituje 1 wiadomość na 1 tool call, a resume odtwarza 1:1.

### Po etapie 2 — bo connector jest właśnie tym, co je rozplątuje

Gemini: trzy defekty rozsmarowane po `parseURI` (ścieżka), switchu w `config.go`
(model), `api/gemini.go` (nagłówek). `case "gemini": // different path structure`
jest wprost objawem brakującej abstrakcji — connector sam buduje swój URL i nagłówki.
Naprawa teraz = wpisanie logiki w switch, który i tak znika. Wymaga dokumentacji
Gemini + realnego wywołania z kluczem.

- [ ] **gemini: `role: "assistant"`** w `contents[]` — Gemini zna tylko `user`/`model` (`convert.go:173-176`)
- [ ] **gemini: brak ścieżki i modelu** — `POST /` zamiast `/v1beta/models/<model>:streamGenerateContent`; `GeminiRequest` nie ma pola `model`, więc `req.Model` jest gubiony
- [ ] **gemini/anthropic: `Authorization: Bearer`** zamiast `x-goog-api-key` / `x-api-key` (`api/gemini.go:59`, `api/anthropic.go:102`) — działa tylko przez proxy OpenAI-style

### Osobno, kiedykolwiek — to decyzje projektowe, nie bugfixy

Wymagają ustalenia konwencji albo dotykają konsumentów (`display/`), więc nie
wchodzą w okolice refactoru.

- [ ] **`IsError` honorowane tylko przez Anthropic** — openai i gemini nie mają natywnego odpowiednika, trzeba ZDECYDOWAĆ konwencję, a nie „naprawić"
- [ ] **bloki `thinking` odrzucane we wszystkich 3 konwerterach** — istotne dla Anthropic extended thinking (wymaga odesłania podpisanych bloków)
- [ ] **`Finish.Reason` nieznormalizowany** — `tool_calls` / `tool_use` / `STOP` / `stop` (gemini miesza wielkość liter)
- [ ] **`ConvertToolsToAnthropic` przy błędzie parsowania zwraca format OpenAI as-is** i loguje globalnym `log.Printf` (`api/anthropic.go:362`)

Do posprzątania przy przenoszeniu (nie bug): `config.go:Stream` robi `for _, e := range p.entries { entry = &e; break }` — poprawne przy `go 1.25`, ale wygląda jak klasyczny aliasing pętli.
