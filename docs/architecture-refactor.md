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

Kluczowe: wymienny jest connector, nie provider — jest jedna implementacja
providera, jawnie konstruowana z wstrzykniętymi zależnościami. Sam `Provider`
zostaje interfejsem, bo inaczej agent byłby związany z typem konkretnym
(patrz odstępstwa etapu 4).

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

## Etap 1 — odwrócenie `agent → display` (0,5d) — ZROBIONE

- [x] `agent.Sink` = kopia dzisiejszego `display.Display` (`agent/sink.go`)
- [x] sygnatury w `agent.go` / `run_once.go` / `fallback.go` / `run_tools.go` na `Sink` (7 miejsc)
- [x] zero zmian w `display/` (typowanie strukturalne załatwia sprawę)
- [x] weryfikacja: `go list -deps ./agent | grep display` → pusto

Goldeny z etapu 0 przeszły bez `-update` — dowód, że zachowanie nietknięte.
`display.Display` zostaje dla call-site'ów; docelowo do usunięcia po etapie 6.

## Etap 2 — pakiet `connector` (1,5–2d) — ZROBIONE

- [x] `connector/connector.go`: `Connector`, `Endpoint`, `Factory`, `Registry` (wartość, nie global)
- [x] `connector/openai.go` + przeniesienie `RichMessagesToChat`
- [x] `connector/anthropic.go` + `RichMessagesToAnthropic` (`ConvertToolsToAnthropic` została w `api/` — używa jej też `anthropic_client.go` i ma stub pod build tagi; przeniesienie wymagałoby zmian w `api/`, czyli wyjścia poza etap)
- [x] `connector/gemini.go` + `RichMessagesToGemini`, `convertToolsToGemini`
- [x] ciała connectorów najpierw tylko wołają dzisiejsze `api.StreamX` (bez zmiany HTTP)
- [x] `dynamicProvider.Stream` skrócone do: URI → klucz → `registry.New` → `conn.Stream` (125 → 39 linii)
- [x] golden files z etapu 0 nadal przechodzą **bez** `-update`

Kanoniczne typy wiadomości (`Message`, `ContentBlock`, `Request`) mieszkają teraz
w `connector`; `providers` trzyma aliasy ze znakiem `=`, więc `agent/`, `session/`,
`display/`, `tools/` i `main` nie wymagały ANI JEDNEJ zmiany. `pakiet api/` nietknięty.

Jedyna świadoma mikro-zmiana zachowania: wysyłka `stream.StreamError` jest teraz
jednolita i robi `select` na `ctx.Done()`. Wcześniej gałąź openai blokowała bez
`select` (anthropic i gemini już miały). Widoczne tylko przy już anulowanym ctx.

## Etap 3 — `HTTPDoer` (1d) — ZROBIONE

- [x] `type HTTPDoer interface{ Do(*http.Request) (*http.Response, error) }` (`api/api.go`)
- [x] `api.StreamX(...)` → metody `ChatStreamer` / `AnthropicStreamer` / `GeminiStreamer`,
      każda z polami `HTTP HTTPDoer` i `Headers map[string]string`
- [x] `ClientFromContext` jako fallback gdy `Endpoint.HTTP == nil` — **fallback ZOSTAJE do etapu 4**
- [x] `connector.Endpoint.HTTP` i `.Headers` faktycznie konsumowane (były martwymi polami po etapie 2)
- [x] wstrzykiwalny klient w `internal/connect/{connect,modelsdev}.go` (`internal/mcp/http.go` miał już pole)
- [x] usunąć martwy `api/client.go` (+ `chat_client.go`, `anthropic_client.go`,
      `gemini_client.go` i ich dwa stuby — razem 1070 linii porzuconej
      równoległej implementacji `Streamer`/`StreamRequest`/`*Client`)
- [x] usunąć martwy `tyciconfig.ProviderURI.FullEndpoint()`
- [x] `default:` w starym switchu potwierdzony jako martwy — `tyciconfig.Parse`
      normalizuje każdy nieznany scheme do `openai` (`uri.go:45-51`, pokryte przez
      `TestParseURI_table`). Fallback na openai w `providers.kindFor` **usunięty całkiem**
- [x] usunąć `api/anthropic_stub.go`, `api/gemini_stub.go`
- [x] build tagi przeniesione z `api/` na poziom `connector/`; rejestracja per-kind
- [x] goldeny z etapu 0 nadal przechodzą **bez** `-update`

### Odstępstwa od planu (świadome)

**`ClientFromContext` i `HTTPClientKey` zostają.** Plan mówił „potem usunąć" — zawężone.
Kontekst jest dziś JEDYNĄ drogą wstrzyknięcia klienta: realny konsument to izolowany
pool połączeń subagenta (`tools/subagent.go:408-415`) plus golden testy. Usunięcie
fallbacku wymaga, żeby ktoś podał `HTTPDoer` przy budowie providera — a provider staje
się strukturą dopiero w etapie 4. Wybór klienta: `if s.HTTP != nil { s.HTTP } else
{ ClientFromContext(ctx) }`, komentarz przy `api.doer()` wskazuje etap 4.

**Build tagi NIE znikają, tylko się przeprowadzają.** Plan zakładał „usunąć build tagi",
ale `make minimal` musi nadal fizycznie nie zawierać kodu anthropic/gemini — inaczej
„minimal" przestaje być minimalny. Zamiast tego:

- `connector/anthropic.go` + `connector/gemini.go` (i ich testy) dostają tagi
  `!noanthropic` / `!nogemini`; `api/anthropic{,_types}.go` i `api/gemini{,_types}.go` też,
- rejestracja w domyślnym rejestrze składa się per-kind: każdy plik connectora dopisuje
  swoją fabrykę z `init()` do `connector.builtinFactories`, więc tag usuwający plik
  usuwa też rejestrację — nie ma jednego miejsca wymieniającego trójkę,
- `Makefile` bez zmian (`minimal` = te same dwa tagi).

Zweryfikowane `go tool nm`: build `-tags "noanthropic nogemini"` nie zawiera ani jednego
symbolu anthropic/gemini (pełny build ma 7), binarka jest o ~72 kB mniejsza.

**Pułapka, którą to odkryło:** po etapie 2 `providers.kindFor` używało `Registry.Has`
z fallbackiem na openai. W buildzie minimalnym URI `anthropic://` pojechałoby wtedy po
cichu connectorem openai — Anthropic-owy request na endpoint chat-completions, czyli
cichy zły request zamiast czytelnego błędu ze stuba. Naprawione: `connector.IsKnownKind`
+ `connector.ErrExcluded` dają dokładnie dawny komunikat („anthropic support excluded at
build time (rebuild without -tags noanthropic)"), a fallback na openai zniknął całkiem.
Testy: `TestDynamicProviderKindFor_*` w `providers/provider_test.go` (działają bez tagów,
bo wstrzykują własny rejestr).

**`providers/wire_golden_test.go` zachowuje tag `!noanthropic && !nogemini`.** Plan
zakładał skasowanie go razem ze stubami. Nie da się: plik asertuje goldeny anthropic
i gemini, których build bez tych connectorów z definicji nie wyprodukuje.

**`connector.Endpoint.Headers` skonsumowane** (plan tego nie wymieniał). Nagłówki są
ustawiane PO domyślnych, więc mogą je nadpisać; mapa jest dziś zawsze pusta, więc bajty
na drucie się nie zmieniają. Dowód, że pole nie jest dekoracją:
`connector/endpoint_http_test.go`.

**Dług zastany naprawiony przy okazji:** `go test -tags "noanthropic nogemini" ./api/`
znów się kompiluje. Helpery `testCtx()` i `as()` przeniesione z plików pod tagami do
nieotagowanego `api/api_test.go`, a testy `TestStreamGemini_*` wydzielone do
`api/gemini_test.go` pod `//go:build !nogemini`. Sprawdzone wszystkie cztery kombinacje
tagów: build + vet + test.

**`internal/connect`:** trzy `&http.Client{}` (2× `connect.go`, 1× `modelsdev.go`)
zastąpione parametrem `HTTPDoer` na fetcherach i jednym `defaultHTTPClient` w
wywołaniach z CLI. Bez `Timeout`, dokładnie jak zastąpione literały.
`fetchModelsDev` przeszło z `client.Get` na `NewRequest`+`Do` (ten sam request).

Zero zmian obserwowalnego zachowania w pełnym buildzie. Jedyna zmiana zachowania
dotyczy buildu minimalnego i jest naprawą opisanej wyżej pułapki: nieznany api_type
(nieosiągalny przez `parseURI`) daje teraz błąd `unsupported api_type` zamiast cichego
przekierowania na connector openai.

## Etap 4 — provider jako struktura (1d) — ZROBIONE

- [x] `providers.Provider`: implementacja staje się jawnie konstruowaną strukturą
      (`Catalog` jako wartość, `AuthSource`, `connectors`, `http`) — **interfejs
      `Provider` zostaje**, patrz odstępstwa
- [x] usunąć `api.ClientFromContext` / `api.HTTPClientKey` — provider wstrzykuje
      `HTTPDoer` do `connector.Endpoint.HTTP`. Izolowany pool przeniesiony
      z `tools/subagent.go` do `main.go:withIsolatedPool`; wstrzyknięcia
      w `providers/provider_test.go` i `providers/wire_golden_test.go` idą przez `Deps.HTTP`
- [x] `AuthSource` jako interfejs (`LiteralAuth` / `AuthFile` / `EnvAuth` / `AuthChain`)
- [x] `providers.Default` zostaje dla CLI; `Catalog` jest wartością, testy budują własny
- [x] goldeny z etapu 0 nadal przechodzą **bez** `-update`

### Odstępstwa od planu (świadome)

**`providers.Provider` ZOSTAJE interfejsem.** „interface → struct" z nagłówka planu
zawężone do implementacji: `dynamicProvider` przestaje sięgać po `defaultConnectors`,
`connect.GetKey` i `os.Getenv`, a dostaje je przez `NewProvider(name, entries, Deps)`.
Gdyby `Provider` przestał być interfejsem, `agent.Run(ctx, p providers.Provider, ...)`
związałby agenta z typem konkretnym — dokładne odwrócenie celu refaktoru — i wywaliłby
fake'i z `main_resolve_test.go` oraz `agent/agent_test.go`. Wymienność zostaje na
poziomie connectora, jak mówi diagram.

**`WithHTTP` NIE wchodzi do interfejsu `Provider`.** Jest metodą konkretnego typu plus
opcjonalnym interfejsem `providers.HTTPInjector`. Inaczej każdy fake providera musiałby
implementować troskę o HTTP, o której agent nie ma prawa wiedzieć. `main.go` robi
type-assert; provider bez tej metody zostaje nietknięty i ma dzisiejsze zachowanie
„brak izolacji" — czyli to, co fake'i mają dziś.

Koszt tego kompromisu, świadomy: wstrzykiwanie transportu jest niewidoczne w kontrakcie
`Provider`, więc implementacja, która nie jest `*dynamicProvider`, po cichu nie dostaje
izolacji i nic tego nie wykryje przy kompilacji. Luka w etapie 5 (provider fallbackowy)
mogła zaistnieć dokładnie dlatego. Podobnie `Deps.HTTP == nil` prowadzi do globalnego
`api.defaultClient` — provider nie jest pełnym właścicielem swojego transportu, bo
normalna ścieżka produkcyjna celowo trzyma jeden klient na proces (reużycie połączeń).
„Wszystko wstrzykiwalne" jest więc prawdą dla wywołującego, który się o to zgłosi.

**`WithHTTP` zwraca kopię, nigdy nie mutuje odbiornika.** Równoległe subagenty
(`subagent(tasks=[...])` → `runTasks` → goroutine per task) współdzielą jedną wartość
providera; mutacja przelałaby pool jednego dziecka do requestów drugiego.
Test: `TestWithHTTP_ReturnsCopy`, `TestWithIsolatedPool_FreshClientPerCall`.

**Ziarnistość izolowanego poolu bez zmian.** Dziś jeden `*http.Client` na
`runSingleTask`. Po przeniesieniu: jeden na wejście w `agentRunner.run`, a `run` jest
wołane dokładnie raz na `RunTask`/`RunTaskWithSystem`, czyli raz na `runSingleTask`.
Równoległe `subagent(tasks=[a,b,c])` nadal tworzy trzy poole.

**Brak realnej różnicy semantycznej po wyjęciu klienta z kontekstu.** Plan podejrzewał,
że klient z kontekstu obejmował *dowolne* wywołanie warstwy `api` w biegu dziecka,
a po zmianie obejmuje tylko strumienie providera. Zweryfikowane: `api` wykonuje HTTP
wyłącznie w trzech miejscach (`chat.go:147`, `anthropic.go:120`, `gemini.go:77`), wszystkie
przez `doer()`, a jedynymi nietestowymi konstruktorami streamerów są trzy connectory
budowane wyłącznie przez `dynamicProvider.Stream`. Pozostali konsumenci HTTP w biegu
dziecka (`tools/web.go`, `internal/mcp`, `internal/connect`) zawsze mieli własnych
klientów i nigdy nie czytali klucza kontekstowego. Zbiór objętych wywołań jest ten sam.

**`doer()` zachowuje osłonę na typed-nil `*http.Client`.** Skasowany
`ClientFromContext` miał `cl != nil`; `Deps.HTTP` to interfejs, więc
`Deps{HTTP: jakisNilowyKlient}` łatwo wyprodukować. Bez osłony byłaby to panika
w `net/http` zamiast dawnego zjazdu na `defaultClient`.

**`api.defaultClient` ZOSTAJE.** To domyślka, nie odczyt kontekstu: provider z `http == nil`
mówi „nie mam własnego klienta" i to jest normalna ścieżka produkcyjna.

**`providers/providers_test.go` nie wymagał przepisania.** Plan mówił „844 linie" — liczba
zastana z przed etapu 2. Plik ma dziś 282 linie i pokrywa `LoadConfig` / `MustLoadConfig` /
`parseURI` / `parseModel`, czyli rzeczy nietknięte przez ten etap. Dopisane zostały testy
`Catalog`; nic nie zostało usunięte.

**Bugi wire-formatu Gemini przeniesione POZA etap 4** (patrz „Bugi znalezione przy etapie 0").
Goldeny są siatką bezpieczeństwa tego refaktoru — celowe pęknięcie ich w tym samym etapie
odebrałoby możliwość odróżnienia „refaktor coś zepsuł" od „zmieniliśmy zachowanie świadomie".

**Nazwy testów: 6 przekształceń, 0 usunięć.** `TestClientFromContext_{DefaultClient,
OverrideFromContext,NilClientInContext}` → `TestDoer_{NoInjectionUsesDefaultClient,
InjectedClientWins,TypedNilClientUsesDefaultClient}` (te same trzy gwarancje przez nową
ścieżkę wstrzyknięcia). `TestChatStreamer_{HTTPFieldWinsOverContext,
NilHTTPFallsBackToContext}` → `...OverDefaultClient` / `...ToDefaultClient` oraz
`TestEndpointNilHTTPUsesContextClient` → `...UsesDefaultClient` — same nazwy stały się
nieprawdziwe po zniknięciu kontekstu.

**Stara notatka „`config.go:Stream` robi `for _, e := range p.entries`" jest nieaktualna.**
Etap 2 zamienił tę pętlę na `findEntry`, który indeksuje `p.entries[i]`. Nie ma czego
sprzątać; notatka skasowana.

**Etap 5 nietknięty świadomie.** `agent/fallback.go` nadal woła globalne
`providers.FindModel`, a `providers.WithProvider` / `ProviderFromContext` zostają —
to zakres etapu 5.

### Weryfikacja

- `go build` + `go vet` + `go test ./... -count=1` zielone we wszystkich czterech
  kombinacjach tagów (brak, `noanthropic`, `nogemini`, `noanthropic nogemini`),
- `gofmt -l .` puste,
- `git diff --stat d724940..HEAD -- providers/testdata` **puste** — wire format przeżył,
- `grep -rn "ClientFromContext\|HTTPClientKey" --include="*.go" .` → nic (także w komentarzach),
- `go tool nm` na buildzie `-tags "noanthropic nogemini"`: zero symboli anthropic/gemini
  (pełny build: 25 unikalnych). Te same liczby na binarce zbudowanej z `d724940`,
  czyli bez regresji względem etapu 3. Uwaga: etap 3 zapisał „pełny build ma 7" —
  inna miara (`grep` po samych nazwach vs po całych liniach `nm`), nie zmiana stanu.

## Etap 5 — fallback poza agentem (0,5d) — ZROBIONE

- [x] `Config.FallbackModels []string` → `Fallbacks []connector.ModelClient` rozwiązane przez wywołującego
      (`commands.go:resolveFallbacks`, `main.go:resolveModelClient`, `internal/workflow/engine.go`)
- [x] `agent/fallback.go` przestaje wołać `providers.FindModel` — iteruje `cfg.Fallbacks`,
      już rozwiązane; jedyny błąd możliwy w pętli to nieudany `Stream`, nie "nie znaleziono"
- [x] `providers.WithProvider`/`ProviderFromContext` + `WithModel`/`ModelFromContext`
      (`providers/context.go`, usunięty) → `connector.WithModelClient`/`ModelClientFromContext` —
      jedna wartość w kontekście, bo `ModelClient` niesie już swój model
- [x] weryfikacja: `agent` nie importuje `providers` (`go list -deps ./agent` — patrz niżej)
- [x] **kryterium akceptacji: provider fallbackowy dziecka też dostaje izolowany pool.**
      `main.go:withIsolatedPool` wiąże teraz provider główny ORAZ każdy fallback z JEDNYM
      prywatnym `http.Client` (współdzielonym w ramach jednego przebiegu dziecka, bo primary
      i fallback nigdy nie działają równolegle). `agentRunner.run` przepuszcza przez ten sam
      wrapper listę fallbacków, która dziś jest zawsze pusta (nazwane subagenty nie mają
      jeszcze podłączonego `GetFallbackModels` — to zostaje poza zakresem, patrz odstępstwa),
      więc mechanizm jest gotowy, zanim ktoś go faktycznie użyje.
      Testy: `TestWithIsolatedPool_WrapsFallbacksWithPrimary`,
      `TestWithIsolatedPool_FallbackPoolDistinctAcrossChildren`.

### Odstępstwa od planu (świadome)

**Etapy 4 i 5 planu ("suggested commit sequence") połączone w jeden commit
zamiast dwóch.** Plan proponował: commit 4 — `Run` bierze `ModelClient`
+ `fallback.go` przestaje wołać `FindModel`; commit 5 — osobno przełączyć
przenoszenie kontekstu. Nie da się rozdzielić bez commitu przejściowego, który
byłby czerwony albo wymuszał tymczasowy dual-write: `agent/run_once.go` woła
`providers.WithProvider(ctx, p)`, gdzie `p` jest `providers.Provider` — w
chwili, gdy `Run` zaczyna przyjmować `ModelClient` (który nie jest
`providers.Provider`), `run_once.go` fizycznie nie ma już czym wywołać
`WithProvider`. Napisanie/odczyt konteksu muszą więc zmienić się w tym samym
kroku co sygnatura `Run` — stąd jeden commit
(`250481e agent: fallback rozwiazywany przez wywolujacego, ModelClient w kontekscie`)
obejmujący oba punkty planu.

**`session/session.go` też przestał importować `providers`, mimo że plan
("Design decisions ALREADY MADE") mówił wprost: "Keep the aliases in
providers — the CLI and session still use them."** Odkryte przy weryfikacji
`go list -deps ./agent`: `agent` importuje `session` (typ `*session.Session`
w `Config.Session`), a `session.go` importował `providers` wyłącznie po
aliasy `RichMessage`/`ContentBlock` (te same typy co `connector.Message`/
`ContentBlock` — `providers` tylko re-eksportuje `connector`). Bez tej zmiany
"`agent` nie importuje `providers`" byłoby prawdziwe tylko dla importów
bezpośrednich, a `go list -deps ./agent | grep providers` i tak by coś
wypisało — czyli nagłówkowe kryterium etapu byłoby fałszywe. Naprawa: to samo
mechaniczne przepisanie na `connector.Message`/`ContentBlock`, które dostał
`agent/` w commicie 3 — zero zmiany zachowania (alias to ten sam typ), test
session/ nie wymagał modyfikacji. `providers` zostaje właścicielem aliasów;
`session` teraz woli własną, bezpośrednią nazwę, tak jak `agent`.

**`resolveModelClient` (dawne `resolveProviderModel`) dostało nową gałąź, bez
odpowiednika w kodzie sprzed etapu.** Stary kod trzymał `providers.Provider`
i `model string` jako DWIE niezależne wartości w kontekście, więc subagent z
jawnym bare-name override (`model` różny od modelu rodzica, bez `/`) po
prostu dostawał `(rodzicProvider, nowyModel)` — provider nie wiedział o
żadnym konkretnym modelu, więc nie było czego przerabiać. `ModelClient` niesie
swój model na trwałe, więc gdy override różni się od `mc.Model()`,
`resolveModelClient` musi odtworzyć nowy `ModelClient` na tym samym providerze
przez `providers.GetProvider(mc.Provider())` (odczyt z globalnego katalogu po
nazwie). Zachowanie funkcjonalnie identyczne — provider rodzica, inny model —
ale ścieżka kodu jest nowa, bo poprzednio nie było jej czym pokryć: żaden
istniejący test tego przypadku nie używał (override w testach i tools/
zawsze był albo `""`, albo pełnym `"provider/model"`). Dodany test:
`TestResolveModelClient_BareOverrideDifferentModelReusesProvider`.

**Dwa martwe pola `fallbackState.active`/`.fullModel` usunięte przy okazji.**
Ustawiane w `agent/fallback.go`, nigdy odczytywane (`grep` to potwierdza).
Nie do uniknięcia: przy zamianie `provider+model+fullModel` na jedno pole
`mc connector.ModelClient` trzeba było dotknąć każdego pola struktury; to nie
jest osobne "ulepszenie na boku", tylko efekt obowiązkowej przebudowy.

**Dług zastany zauważony i ŚWIADOMIE nietknięty:** `agent.Config.ProviderName`
jest zapisywany w sześciu miejscach (`commands.go`, `interactive.go` ×2,
`tui_mode.go` ×3) i nigdzie w całym repo nie jest odczytywany — pole
write-only sprzed tego etapu. Nie naprawiane tutaj: nie ma z tym nic
wspólnego zakres Etapu 5, a "nie commitować nieproszonych poprawek" jest
twardym ograniczeniem tego zadania.

**`Config.Model` i `Config.ProviderName` zostają w strukturze, mimo że `agent`
już ich wewnętrznie nie potrzebuje** (`runOnce`/`fallback.go` czytają
`mc.Model()`/`mc.Provider()`). Powód: wywołujący (`prompt_mode.go:29`,
`interactive.go`, `tui_mode.go`) czytają/piszą `cfg.Model` do własnych celów
(nazwa sesji, przełączanie modelu), niezależnych od tego, jak `Run` go
zużywa. Usunięcie pola złamałoby te call site'y bez żadnej korzyści.

Rozstrzygnięcie obu odłożone na **przed etap 6** — patrz „Do posprzątania PRZED
`Conductor`" w sekcji etapu 6. Powód, dla którego nie zostaje to tu na zawsze:
`Conductor` przenosi `agent.Config` do nowego API, a pole, którego agent nie
czyta, w nowym API wygląda jak kontrakt. `ProviderName` nie ma przy tym nawet
tego usprawiedliwienia co `Model` — nie czyta go nikt, ani agent, ani wywołujący.

### Weryfikacja

- `go build` + `go vet` + `go test ./... -count=1` zielone we wszystkich czterech
  kombinacjach tagów (brak, `noanthropic`, `nogemini`, `noanthropic nogemini`),
- `go test -race ./agent/ ./providers/ ./tools/ .` zielone,
- `gofmt -l .` puste,
- `git diff --stat a04f9a8..HEAD -- providers/testdata` **puste** — wire format przeżył,
- `go list -deps ./agent | grep decodo/tyci/providers` → **puste** (dowód nagłówkowy etapu),
- nazwy testów: 6 przekształceń 1:1 (`TestResolveProviderModel_*` →
  `TestResolveModelClient_*`), 16 dodanych (nowe pakiety `connector`/`providers`
  + jeden nowy przypadek `resolveModelClient` + dwa testy izolacji fallbacków),
  0 usunięć bez odpowiednika.

## Etap 6 — frontend jako sterownik (2d)

**Rozstrzygnięte PRZED tym etapem:** `Provider.FreeModels()` było w produkcji
martwe (jedyna nietestowa implementacja zwracała `nil` bezwarunkowo).
**Decyzja: wycięte, nie zaimplementowane** — patrz „Sprzątanie przed `Conductor`"
niżej. Metoda nie wchodzi do projektu `Conductor`.

### Sprzątanie przed `Conductor` — ZROBIONE

`Conductor` projektuje się wokół `agent.Config` i `connector.ModelClient`. Trzy
z poniższych to pola i abstrakcje, które etap 5 zostawił w stanie „istnieje, ale
nikt tego nie czyta". Przeniesienie ich do nowego API utrwaliłoby błąd, więc
kolejność była: najpierw sprzątanie, potem `Conductor`.

- [x] **`agent.Config.Model` — agent to ignorował.** Pole usunięte z `Config`.
      Model jest wyłącznie właściwością `connector.ModelClient` podanego do `Run`
      (`mc.Model()`); wywołujący, którzy potrzebują nazwy do własnych celów,
      trzymają własną zmienną. `runPrompt` dostał jawny parametr `modelName`
      (czytał `cfg.Model` przy zakładaniu sesji i przy budowie klienta);
      `commands.go` / `interactive.go` / `tui_mode.go` miały już `provider` +
      `modelName` obok `cfg`, więc zapisy do `cfg` były czystą duplikacją;
      `main.go` i `internal/workflow/engine.go` tylko pisały. Dwa źródła prawdy →
      jedno. Komentarz „agent tego nie czyta" świadomie NIE został zostawiony —
      to właśnie stan, który usuwaliśmy.
      Commit `8d90b5d`.
- [x] **`agent.Config.ProviderName` — martwe i mylące.** Usunięte tym samym
      commitem. Metadane sesji biorą `mc.Provider()` (`run_once.go`).
- [x] **`Provider.FreeModels()` wycięte.** Metoda wypadła z interfejsu, z
      `dynamicProvider` i z trzech atrap w testach; zniknęła druga pętla w
      `Catalog.FindModel` (razem z komentarzem, który opisywał ją jakby była żywa)
      oraz cztery martwe pętle w CLI. Zachowawcze *przez konstrukcję* — każde
      użycie iterowało po `nil`; dowód per użycie w opisie commita `76a8629`.
      Jedyne miejsce z wpływem na sterowanie, `provider list --models`, miało
      warunek `len(models) == 0 && len(freeModels) == 0`, który redukuje się do
      `len(models) == 0`, więc komunikat „(no models)" pojawia się dokładnie tam,
      gdzie dotąd.
- [x] **`providers.Provider` jest już wyłącznie interfejsem katalogu.**
      `Stream` wypadło z interfejsu; `Provider` = `Name` + `IsConfigured` +
      `Models` + `Client(model) connector.ModelClient`. Jedyną drogą do wysłania
      requestu jest `connector.ModelClient`. Commity `bec3622` (fabryka staje się
      metodą) i `60c32db` (`Stream` zdjęte z interfejsu).
- [x] **`HTTPInjector` — trzy ogniwa → jedno.** `providers.HTTPInjector` usunięty
      całkiem, `dynamicProvider.WithHTTP` → nieeksportowane `withHTTP`
      zwracające typ konkretny. Zostaje jedno ogniwo, `connector.HTTPInjector`
      na `modelClient`, i jedna zamierzona assertion w
      `main.go:withIsolatedPool` (atrapy `ModelClienta` jej nie spełniają i mają
      przechodzić bez izolacji, jak dotąd). Żeby nie mogła po cichu przestać
      działać dla ścieżki produkcyjnej, `providers/client.go` ma
      `var _ connector.HTTPInjector = (*modelClient)(nil)` — awaria runtime
      zamieniona na awarię builda. Commit `60c32db`.

#### Projekt podziału `Provider` i dlaczego tak

`Provider` **zostaje interfejsem** (ograniczenie z etapu 4: struktura zepsułaby
każdą atrapę i wciągnęła typ konkretny do sygnatur). Podział wygląda tak:

```
Provider (katalog)                 connector.ModelClient (transport)
  Name() / IsConfigured()            Provider() / Model()
  Models()                           Stream(ctx, Request)
  Client(model) ──────────────────▶  (+ opcjonalnie connector.HTTPInjector)
```

Kluczowa decyzja: **fabryka `ModelClient` jest metodą `Provider`, nie funkcją
pakietową.** Dawne `providers.Client(p Provider, model string)` zniknęło.
Powód jest wprost o cichej awarii: funkcja przyjmująca interfejs `Provider`,
który nie ma już `Stream`, musiałaby transport *odnaleźć za* interfejsem, czyli
przez type assertion — a nieudana assertion w takim miejscu degraduje bez
błędu. Jako metoda jest sprawdzana przez kompilator: każda implementacja
`Provider` musi umieć wydać swojego klienta i sama decyduje, co ten klient
potrafi. To także dlatego atrapy w testach są dziś *lżejsze*, nie cięższe —
katalogowa atrapa nie dziedziczy transportu, o który nie prosiła.

Skutek dla łańcucha HTTP: `modelClient` trzyma `*dynamicProvider` (typ
konkretny), więc `modelClient.WithHTTP(h)` = `c.p.withHTTP(h).Client(c.model)` —
oba skoki statyczne, nie ma czego nie dopasować. Zostaje jedna assertion, w
`main.go`, i jest to jedyne miejsce, gdzie „brak izolacji" jest poprawną
odpowiedzią (atrapy). `var _` w `providers/client.go` gwarantuje, że nigdy nie
jest to odpowiedź dla klienta z produkcji.

Wypadło przy tym z zasięgu `dynamicProvider.Stream`: metoda została na typie
**nieeksportowanym**, więc spoza pakietu `providers` nie ma żadnej drogi do
strumienia poza `connector.ModelClient`. Testy w `providers/`, które budowały
providera przez `NewProvider`, streamują teraz przez `p.Client(model).Stream(...)`,
czyli przez ścieżkę produkcyjną — dotyczy to też `wire_golden_test.go`.

#### Odstępstwa od planu (świadome)

**Zadania „rozdziel `Provider`" i „skróć łańcuch `HTTPInjector`" nie dały się
rozdzielić na dwa commity po granicy zadań.** Plan przewidywał osobny commit na
każde. Granica nie jest jednak szwem, który się kompiluje: w chwili, gdy
`Provider` traci `Stream`, `modelClient` musi trzymać typ konkretny (trzymanie
`Provider` + assertion do jakiegoś „streamera" odtworzyłoby dokładnie ten tryb
cichej awarii, który usuwamy), a `modelClient` z polem `*dynamicProvider`
unieważnia sygnaturę `providers.HTTPInjector` (`WithHTTP(HTTPDoer) Provider`) —
Go nie ma kowariancji zwracanego typu, więc interfejs przestaje być spełniany
przez cokolwiek i musi zniknąć w tym samym commicie. Rozbicie poszło więc po
szwie, który *da się* skompilować: `bec3622` wprowadza nową fabrykę
(`Provider.Client`), `60c32db` usuwa starą drogę (`Stream` z interfejsu +
`providers.HTTPInjector`). Każdy z dwóch commitów jest zielony osobno.

**`dynamicProvider.Stream` zostało eksportowane.** Zdjęcie go z interfejsu
wystarcza: typ jest nieeksportowany, a `NewProvider` zwraca `Provider`, więc
metoda jest nieosiągalna spoza pakietu. Przemianowanie na `stream` dołożyłoby
tylko kolizję czytelniczą z importowanym pakietem `stream`.

**Ziarnistość i semantyka izolowanego poolu bez zmian.** `withIsolatedPool`
nadal daje jeden `*http.Client` na wejście w `agentRunner.run` (czyli na
`RunTask`/`RunTaskWithSystem`), wspólny dla modelu głównego i wszystkich jego
fallbacków. Oba testy izolacji fallbacków z etapu 5 przechodzą bez zmian w
asercjach; zmieniła się tylko atrapa — `recordingInjector` nagrywa wstrzyknięty
klient na poziomie `ModelClient`, a nie `Provider`, bo `withIsolatedPool` widzi
wyłącznie `ModelClienty` i to jest poziom, na którym jego kontrakt istnieje.

**Nazwy testów: 1 przekształcenie 1:1, 1 dodanie, 2 usunięcia (netto −1).**
`TestNewProvider_ImplementsHTTPInjector` → `TestProviderClient_ImplementsHTTPInjector`
(ta sama gwarancja przeniesiona z providera na klienta, plus sprawdzenie, że
wstrzyknięty klient faktycznie ląduje w `Endpoint.HTTP`).
`TestClient_WithHTTPNoopWhenProviderIsNotInjector` **usunięty bez następcy**:
gałąź, którą opisywał, przestała istnieć (każdy `modelClient` owija
`dynamicProvider`, który zawsze jest `HTTPInjectorem`). Gwarancję „`ModelClient`
bez `HTTPInjectora` przechodzi `withIsolatedPool` nietknięty" trzyma
`TestWithIsolatedPool_PassesThroughNonInjector` w `main_resolve_test.go`.
Atrapa `recordingProvider` (nie test) usunięta jako niepotrzebna — po podziale
atrapa `Providera` wydaje WŁASNEGO klienta, więc nie dotknęłaby `modelClient`
ani razu; `TestClient_{ProviderAndModel,StreamForcesBoundModel}` idą teraz przez
prawdziwy provider z nagrywającym connectorem.

**`TestCatalog_FindModelBareName` stracił przypadek „freebie"** — asercja
opisywała usuniętą pętlę `FreeModels`. Test i jego dwie pozostałe asercje
zostają.

#### Weryfikacja

- **4/4 commitów zielonych osobno.** Sprawdzone w jednorazowym
  `git worktree --detach`, commit po commicie: `go build ./... && go vet ./... &&
  go test ./... -count=1 && gofmt -l .` (puste) + `go build` z tagami
  `noanthropic`, `nogemini`, `noanthropic nogemini`. Worktree usunięty.
- `go build` + `go vet` + `go test ./... -count=1` zielone we wszystkich czterech
  kombinacjach tagów (brak, `noanthropic`, `nogemini`, `noanthropic nogemini`),
- `go test -race ./agent/ ./providers/ ./tools/ .` zielone,
- `gofmt -l .` puste,
- `git diff --stat ace8e16..HEAD -- providers/testdata` **puste** — wire format
  przeżył, ani jednego `-update`,
- `go list -deps ./agent | grep decodo/tyci/providers` → **puste**
  (kryterium nagłówkowe całego refaktoru),
- `grep -rn "FreeModels" --include="*.go" .` → dwa komentarze opisujące usunięcie,
  zero kodu; `grep -rn "providers.HTTPInjector"` → jeden komentarz historyczny
  (opis skróconego łańcucha w `providers/client.go`), zero kodu,
- nazwy testów: 1027 → 1026 (`comm` na posortowanych listach `func Test*` przed
  i po): 1 przekształcenie 1:1, 1 dodany, 2 usunięte — wyliczone wyżej.

#### Znalezione po drodze, ŚWIADOMIE nietknięte

- **`subagentDefaultMaxIterations` w `main.go:125` jest martwą stałą** —
  zdefiniowana jako alias `tools.DefaultSubagentMaxIterations` i nigdzie nie
  używana (`tools.ResolveMaxIter` robi to samo po stronie `tools/`). Nie ruszane:
  poza zakresem.
- **`display.ProviderModels` dostaje dziś tylko modele płatne** — po wycięciu
  `FreeModels` nie ma już ścieżki, którą TUI mogłoby dostać model oznaczony jako
  darmowy. Jeśli „free models" mają kiedyś wrócić, muszą wrócić jako właściwość
  wpisu w katalogu (`ModelEntry`), nie jako druga metoda interfejsu — poprzedni
  kształt zgnił właśnie dlatego, że nikt nie miał czym go wypełnić.

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
- [ ] wycofać `api.defaultClientProvider` — mutowalna zmienna globalna istniejąca
      wyłącznie jako seam testowy (`api/api_test.go:757-763` podmienia ją i
      przywraca w `defer`). Dziś bezpieczna, bo w `api/` nie ma ani jednego
      `t.Parallel()`, ale to bezpieczeństwo z przypadku. Connector testowy
      wstrzyknięty przez `Deps.HTTP` pokrywa ten sam przypadek bez globalu.

---

## Uwagi

Ryzyka:
- etap 2 przenosi konwersje wiadomości — najbardziej podatne na cichy regres (stąd etap 0)
- `agent/agent_test.go` (1216 linii) do przepisania — mechaniczne, ale objętościowe.
  `providers/providers_test.go` figurowało tu jako 844 linie: to liczba sprzed
  rozbicia w etapie 2, dziś plik ma 282 linie i etap 4 nie musiał go przepisywać.

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

### Osobne zadanie PO etapie 4 — nie „przy okazji" żadnego etapu

Gemini: trzy defekty rozsmarowane po `parseURI` (ścieżka), switchu w `config.go`
(model), `api/gemini.go` (nagłówek). `case "gemini": // different path structure`
jest wprost objawem brakującej abstrakcji — connector sam buduje swój URL i nagłówki.
Wymaga dokumentacji Gemini + realnego wywołania z kluczem.

Znacznik wędrował: etap 2 → 3 → 4. Zatrzymany tutaj jako **samodzielne zadanie po
etapie 4**, bo goldeny są siatką bezpieczeństwa refaktoru: naprawa wire-formatu
w tym samym etapie co przenoszenie kodu odbiera możliwość odróżnienia „przeniesienie
coś zepsuło" od „zmieniliśmy zachowanie świadomie". `TODO` w `connector/gemini.go`
zostaje na miejscu do tego czasu.

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

---

## Dług zastany (nie nasza regresja, znalezione po drodze)

- [x] `go test -tags "noanthropic nogemini" ./api/` **nie kompilował się** — `testCtx()`
  siedzi w `api/anthropic_test.go` (plik z `//go:build !noanthropic`), a używa go
  `api/api_test.go`. Zweryfikowane na czystym drzewie przed etapem 2: ten sam błąd.
  `go build -tags ...` przechodzi, więc `make minimal` działa; problem dotyczył tylko
  uruchamiania testów z tagami. Naprawione w etapie 3 (helpery przeniesione do
  nieotagowanego pliku, testy gemini wydzielone pod `!nogemini`).
- [ ] `gofmt` całego repo zrobiony w osobnym commicie (8caa1ff) — przed etapem 2.
- [ ] **`IsConfigured()` powtarza lookup niezmienny w pętli.** `p.authSource().Key(p.name)`
  nie zależy od `e`, a stoi w `for _, e := range p.entries` — dla providera bez klucza
  wykonuje się tyle razy, ile ma modeli (617 dla `nano-gpt`). `connect.GetKey` →
  `LoadAuth()` czyta i parsuje `auth.json` przy każdym wywołaniu, bez cache.
  Zmierzone na realnym katalogu (128 providerów, 3827 modeli): `FindModel` na
  nietrafionej nazwie bez prefiksu = **11,8 ms** i ~3800 odczytów pliku. To O(modele)
  tam, gdzie potrzeba O(providerzy). Stary kod miał identyczną pętlę — etap 4 tylko
  uczynił niezmienność widoczną. Naprawa: wyciągnąć wywołanie przed pętlę + dekorator
  z cache na `AuthSource` (to jest właśnie miejsce, w którym takie coś należy).
  Nie pilne: 11,8 ms nikogo nie boli, page cache to amortyzuje.
- [ ] **`IsConfigured` sprawdza token z URI surowo, `Stream` go rozwiązuje.** Wpis
  z nierozwiązywalnym `$FOO` pokazuje się jako skonfigurowany i wywala się dopiero
  przy żądaniu. Asymetria zastana, świadomie zachowana w etapie 4 (inaczej
  `provider list` zacząłby ukrywać providerów, których użytkownik skonfigurował) —
  do rozstrzygnięcia jako konwencja, nie do „naprawienia" po cichu.
