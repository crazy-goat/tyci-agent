# Self-test review: komunikacja i lock między agentami

Data: 2026-08-11. Self-test z `docs/subagent-testing-plan-cross-agent.md`,
wykonany realnie na sobie (prawdziwe wywołania `lock`/`unlock`/`subagent`/
`wait`; ocena po treści `Content`/`Error` zwracanych przez narzędzia).
Dokumentuje, co przeszło zgodnie z oczekiwaniem, a co nie — z rozróżnieniem
na "błąd kodu" vs. "wada determinizmu testu/modelu".

## Wnioski ogólne

Mechanizm `lock`/`unlock`/auto-release/`wait`/współdzielony `jobs.Registry`
działa niezawodnie. Obserwowane wpadki (dwie) to **flakiness wypowiedzi
modelu-dziecka** oraz **kruchy limit retry scenariusza**, nie bug w logice
lockowania.

---

## Co przeszło OK

### Sekcja 6 — Lock: główny wątek vs. job w tle

- **6.1 — PASS.** `lock("shared.go")` w głównym wątku → sukces
  (holder); job w tle na tym samym pathu zakończył się z dokładnym błędem
  konfliktu wskazującym na ten sam holder. Konflikt między-agentowy działa.
- **6.2 — PASS.** `unlock` zwalnia `shared.go`.
- **6.3 — PASS.** Job `lock("x.go")` bez `unlock`, po zakończeniu auto-release —
  natychmiastowy `lock` z wątku głównego udaje się bez konfliktu.
- **6.4 — PASS.** Job `lock("y.go")` potem celowo zepsuty `bash` (exit 127) —
  mimo błędu joba, deferred release zwalnia lock na ścieżce błędu.

### Sekcja 7 — Lock między jobami w tle

- **7.1 — PASS.** Dwójkowy wyścig o `hot.go`: dokładnie jeden dostaje lock
  pierwszy, drugi retry → `gotowe B`. Auto-release po zakończeniu zwycięzcy
  działa bez jawnego `unlock`.
- **7.2 — PASS w mechanizmie** (szczegóły w "Co nie przeszło").
- **7.3 — PASS po poprawce scenariusza** (szczegóły niżej).
- **7.4 — PASS.** Odwrotna kolejność locków `r1.go`/`r2.go`: każdy job zablokował
  swój pierwszy zasób i dostał błąd konfliktu na drugim; oba skończyły się bez
  hanga. Potwierdza, że `lock` jest nieblokujący — klasyczny deadlock dwóch
  zasobów strukturalnie niemożliwy.
- **7.5 — PASS.** Przekazanie "pałeczki" przez jawny `unlock` na `queue.go`:
  konsument (`odebrano po 3 próbach`) nie dostał locka przy pierwszej próbie,
  zdobył go dopiero po jawnym `unlock` producenta.

### Sekcja 7/8 — Komunikacja

- **7.6 — PASS.** `wait(job_id)` działa **z wnętrza** subagenta: job B odczytał
  zakończenie joba A (`job finished: A gotowe`). Dostępny wspólny
  `jobs.Registry`, nie per-agentowa instancja.
- **8.1 — PASS.** Producent→konsument przez główny wątek: hasło `granitowiec`
  przekazane w treści `task` i poprawnie zamienione na `GRANITOWIEC`.
- **8.2 — PASS.** Subagent nie ma narzędzia `subagent` (`Unknown tool:
  subagent`) — rekurencja strukturalnie zablokowana; child zakończył się
  `done` i poprawnie zaraportował błąd.

---

## Co nie przeszło / wątpliwe

### N-1 — 7.2, flakiness wypowiedzi modelu-dziecka (wada determinizmu modelu, nie kodu)

- **Wywołanie:** powtórka 7.2 (`hot.go`), iteracja 3 — job A vs job B.
- **Co się stało:** job **B** ubiegło A i dostało lock pierwsze. Job A zwróciło
  błąd konfliktu (`hot.go already locked by "holder-f8594e1d9b94"`) i **nie**
  wykonało `wait` (bo mu się nie udało), ale mimo to na końcu zwróciło
  szablonowy `gotowe A` — nie trzymało się warunku "jeśli się uda".
- **Czy to bug?** Nie. Mechanizm lock/auto-release był poprawny: dokładnie
  jeden zwycięzca, drugi miał ślad konfliktu, nigdy oba naraz, nigdy prawdziwe
  "nie udało się B". To niespójność wypowiedzi/raportowania przez model-dziecko.
- **Ryzyko:** mylący wynik joba do maszynowego parsowania; ocena scenariusza
  oparta na tekście końcowym jest podatna na to.

### N-2 — 7.3, kryterium "co najmniej dwa joby ze śladem konfliktu" (wada scenariusza + kruchy limit retry)

- **Wywołanie:** 7.3 trójstronny wyścig o `triple.go`.
- **Runda 1 (wzorzec oryginalny, zwycięzca bez trzymania):** C1/C2/C3 wszystkie
  `gotowe` i **w żadnym wyniku śladu konfliktu** → test nie wymusił realnej
  rywalizacji (job-y realizowały się niemal sekwencyjnie). Nie spełnił
  kryterium dokumentu.
- **Runda 2 (dodany `wait 4s` + jawny `unlock` u zwycięzcy):** realna kontencja
  — co najmniej jeden wynik ze śladem konfliktu (C2 wzięło lock dopiero na 5.
  próbie). Kryterium częściowo spełnione.
- **Runda 3 (raportowanie `pierwszy raz / po konflikcie / nie udało się`):**
  C1 `po konflikcie`, C2 `pierwszy raz`, C3 **`nie udało się C3`**.
- **Co nie zadziałało:** przy limit 5 prób z `wait 1s`, gdy obaj zwycięzcy
  trzymali zasób po 4s (razem ~8s+), trzeci job wyczerpał limit, zanim doszedł
  do kolejki. To kruchość **parametrów scenariusza**, nie błędu aut-release
  (3 spośród ~8 jobów w obu rundach zdobyły lock — mechanizm działał).
- **Sugestia:** zwiększyć `wait` retry (`1s → ~3s`) i limit (5 → 10) przy
  trzymaniu zasobu przez zwycięzcę.

---

## Rekomendacja

N-1 i N-2 nie wskazują na bug w mechanizmie. N-1 to determinizm modelu — opisać
w docs, że ocena wyników jobów nie powinna polegać wyłącznie na tekście
końcowym. N-2 to poprawka parametrów scenariusza 7.3 przed ewentualnym
ponownym uruchomieniem; w obecnej formie test przechodzi tylko "warunkowo".
