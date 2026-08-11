# Plan testowy: subagenci, joby w tle, lock, /btw

Ręczny plan testów manualnych dla funkcjonalności dodanych/zmienionych w tej
rundzie prac: `jobs/`, `eventbus/`, `tools/wait.go`, `locks/` + `tools/lock.go`,
async spawn subagenta (`subagent(async: true)`), panel jobów w tle w TUI
(`Ctrl+B`) i `/btw`. Każdy scenariusz: kroki, oczekiwany wynik, gdzie szukać
problemu jeśli wynik się nie zgadza.

Uruchamiaj `tyci` w trybie TUI (nie `--print`/console) dla scenariuszy
dotyczących paneli/modali — te tryby nie mają UI do zaobserwowania.

## 0. Baseline — nic nie powinno się zepsuć

Cel: upewnić się, że stary, synchroniczny `subagent` zachowuje się identycznie
jak przed całą tą rundą zmian.

- [ ] **0.1** `subagent(task: "policz do 5 i zwróć wynik")` (bez `async`) —
      blokuje turę, modal subagenta pokazuje żywy stream, wynik wraca jako
      tekst w tej samej turze.
- [ ] **0.2** `subagent(tasks: [{task:"A"},{task:"B"}])` (dwa równoległe, sync)
      — oba się wykonują równolegle, modal pokazuje wymieszany strumień z obu
      (znana, zaakceptowana wada — patrz `docs/architecture-refactor.md` /
      wcześniejsze ustalenia), wynik to JSON-owa tablica dwóch rezultatów.
- [ ] **0.3** `subagent(agent: "reviewer", task: "...")` — named agent z
      `internal/agentdefs/builtin/reviewer.md` działa: model/tools/temperature
      z frontmatteru są respektowane.
- [ ] **0.4** `subagent(agent: "nieistniejący", task: "x")` — twardy błąd
      ("agent not found"), nie cichy fallback do zwykłego subagenta.

## 1. Async spawn (`subagent(async: true)`)

- [ ] **1.1** `subagent(task: "coś co potrwa ~30s", async: true)` — tura
      **natychmiast** dostaje wynik z `job_id`, nie czeka na zakończenie.
- [ ] **1.2** Zaraz po 1.1: **panel jobów w tle** (dolny pasek, nad polem
      wpisywania) pokazuje nowy wpis ze statusem `running`.
- [ ] **1.3** Po zakończeniu joba: panel aktualizuje status na `done` (lub
      `failed`/`truncated`) na żywo, bez odświeżania/interakcji.
- [ ] **1.4** `Ctrl+B` otwiera modal z listą wszystkich jobów w tle (najnowsze
      na górze); `Enter` na wpisie pokazuje `Result`/`Err` posczytu.
      Jeśli job wciąż `running` — modal pokazuje "still running", nie pusty
      wynik.
- [ ] **1.5** **Regresja, którą naprawiliśmy**: podczas gdy job z 1.1 jeszcze
      trwa, sprawdź że **nic nie leci** do zamkniętego bloku narzędzia w
      głównym widoku transkryptu (blok "subagent" powinien pokazywać się jako
      zakończony natychmiast po zwróceniu `job_id`, i nie powinien później
      "dopisywać się" żywym tekstem z joba w tle).
- [ ] **1.6** `subagent(tasks: [{task:"A", async:true}, {task:"B", async:false}])`
      (mieszane) — twardy błąd "cannot mix async and non-async", zero jobów
      wystartowanych.
- [ ] **1.7** `subagent(tasks: [{task:"A", async:true}, {task:"B", async:true}])`
      — oba starują natychmiast, dwa osobne `job_id` w wyniku, panel pokazuje
      dwa wpisy.
- [ ] **1.8** Panel/modal pusty (zero-height, nie zajmuje miejsca) gdy nikt
      nigdy nie użył `async: true` w bieżącej sesji.

## 2. `wait`

- [ ] **2.1** `wait(seconds: 5)` (bez `job_id`) — model dostaje kontrolę z
      powrotem po ~5s z komunikatem "waited 5s; check status now.".
- [ ] **2.2** `wait(seconds: 5, note: "czekam na build")` — `note` pojawia się
      w zwróconym komunikacie.
- [ ] **2.3a** `wait(seconds: 0)` — przycinane do `MinWaitSeconds` (1), wraca po
      ~1s z adnotacją o przycięciu.
- [ ] **2.3b** `wait(seconds: 99999)`, **ale przerwij ESC-em po ~5s** zamiast
      czekać na koniec — nie czekaj pełnych przyciętych 1800s (`MaxWaitSeconds`)
      w teście manualnym. Sprawdź tylko, że wynik zawiera adnotację o przycięciu
      do maksimum (sam clamp jest już pokryty testem jednostkowym
      `TestWaitTool_ClampsHigh`, nie trzeba tego przesiadywać ręcznie).
- [ ] **2.4** Spawn joba przez 1.1, potem `wait(job_id: "<id z 1.1>", seconds: 60)`
      — jeśli job skończy się przed 60s, `wait` wraca **od razu** z jego
      wynikiem (nie czeka pełnych 60s).
- [ ] **2.5** `wait(job_id: "<id joba który jeszcze trwa>", seconds: 3)` —
      wraca po 3s z "still running after 3s (job_id=...). Call wait again...",
      **Success: true** (to nie błąd).
- [ ] **2.6** `wait(job_id: "nieistniejące-id", seconds: 5)` — błąd "unknown
      job_id".
- [ ] **2.7** ESC/anulowanie w trakcie `wait(seconds: 60)` — przerywa
      natychmiast (nie czeka do końca), zwraca "wait cancelled after ~Ns".

## 3. `lock` / `unlock`

- [ ] **3.1** `lock(path: "foo/bar.go")` — sukces, `Content` zawiera `holder`
      (np. `holder-abc123`) — zanotuj go do kroków niżej.
- [ ] **3.2** Drugie `lock(path: "foo/bar.go")` (inny holder, ta sama ścieżka)
      **w trakcie gdy pierwszy lock jeszcze trwa** — błąd z informacją kto
      trzyma i od kiedy/do kiedy.
- [ ] **3.3** `unlock(path: "foo/bar.go", holder: "<holder z 3.1>")` — sukces,
      ścieżka odblokowana; kolejny `lock` na tę samą ścieżkę już się udaje.
- [ ] **3.4** `unlock(path: "foo/bar.go", holder: "zly-holder")` — błąd (holder
      się nie zgadza), lock pozostaje aktywny.
- [ ] **3.5** `lock(path: "x", seconds: 3)` — po ~3s ścieżka automatycznie się
      odblokowuje (kolejny `lock(path:"x")` bez czekania na `unlock` się udaje).
- [ ] **3.6** `lock(path: "x")` **bez** `seconds` — blokada trzyma się do końca
      sesji/kontekstu (nie wygasa sama w krótkim czasie).

## 4. `/btw`

- [ ] **4.1** W trakcie normalnej rozmowy: `/btw jakie mamy dziś testy jednostkowe w tools?`
      — modal otwiera się **od razu**, pokazuje żywy stream odpowiedzi.
- [ ] **4.2** W trakcie trwania 4.1 (modal btw otwarty lub zamknięty), pisz
      dalej w głównym wątku — **główny wątek nie jest zablokowany**, można
      normalnie kontynuować rozmowę.
- [ ] **4.3** Po zakończeniu btw z 4.1: sprawdź, że odpowiedź btw **nigdy nie
      pojawia się** w głównej historii/transkrypcie — to czysto boczna gałąź.
- [ ] **4.4** Zamknij modal btw (ESC) w trakcie gdy jeszcze streamuje — job
      leci dalej w tle (sprawdź przez panel z sekcji 1 albo przez ponowne
      otwarcie z listy), nie jest ubijany przez samo zamknięcie okna.
- [ ] **4.5** Samo `/btw` (bez pytania) — otwiera **listę** poprzednich btw z
      bieżącej sesji (pytanie, status, skrócony fragment odpowiedzi).
- [ ] **4.6** Z listy z 4.5: wybór starego wpisu (Enter) pokazuje jego pełną
      treść w trybie podglądu (statyczny, nie żywy jeśli już done).
- [ ] **4.7** `/btw` zadane zaraz po tym, jak w rozmowie padł jakiś
      kontekstowy fakt (np. nazwa pliku) — sprawdź że btw **widzi** ten
      kontekst (odpowiada trafnie, bez pytania "o czym mowa") — fork kopiuje
      `msgs` w momencie wywołania.
- [ ] **4.8** Dwa `/btw` odpalone jeden po drugim (drugi zanim pierwszy się
      skończy) — oba działają niezależnie, nie nadpisują sobie nawzajem stanu
      modala/streamu.

## 5. Rzeczy przekrojowe / edge case'y

- [ ] **5.1** Restart `tyci` (nowy proces) — panel jobów pusty, lista btw
      pusta (wszystko in-memory, nic nie przetrwało restartu — to oczekiwane,
      nie bug).
- [ ] **5.2** Zamknięcie `tyci` (Ctrl+C) **podczas** gdy job async z 1.1 albo
      btw z 4.1 jeszcze trwa — proces powinien się zamknąć bez zawieszenia
      (goroutine jest odcięta od kontekstu sesji, ale nie powinna blokować
      shutdownu).
- [ ] **5.3** Tryb `--print`/console (nie-TUI): `subagent(async: true)`
      nadal zwraca `job_id` poprawnie mimo braku panelu do pokazania go
      (panel istnieje tylko w TUI — sprawdź że to nie crashuje w trybie bez
      TUI).
- [ ] **5.4** `go test -race ./...` w repo — zielone jako baseline przed
      testami manualnymi z tego dokumentu.

## Co NIE jest jeszcze zaimplementowane (nie testować, spodziewany brak)

- Auto-`/compact` przy 95% kontekstu — zaplanowane, niezaimplementowane.

## 6. `ask`/`answer`, `report_progress`, `resume`

- [ ] **6.1** W trakcie trwania joba async (spawn przez 1.1): job wywołuje
      `ask(question: "...")` — panel jobów w tle pokazuje status
      `waiting_answer` (zamiast `running`), a `wait(job_id: "<id>")` zwraca
      komunikat zawierający dokładną treść pytania i instrukcję użycia
      narzędzia `answer` z tym `job_id`.
- [ ] **6.2** Zaraz po 6.1: wywołaj `answer(job_id: "<id z 6.1>", text: "...")`
      z głównego wątku (albo z innego agenta) — job odblokowuje się
      natychmiast, `ask` w środku joba dostaje dokładnie ten tekst z powrotem,
      status wraca na `running`, a finalny wynik joba (widoczny przez
      `wait`) odzwierciedla otrzymaną odpowiedź.
- [ ] **6.3** `ask` na który **nikt nie odpowiada** — job **nie wisi w
      nieskończoność**: odblokowuje się sam po osiągnięciu własnego limitu
      czasu joba (600s dla async subagenta), zwracając komunikat mówiący
      wprost, że nie było odpowiedzi i że agent ma kontynuować na własną rękę.
      (W teście manualnym nie czekaj pełnych 600s — testy jednostkowe/
      integracyjne już pokrywają to na krótszym, wstrzykniętym deadlinie;
      manualnie wystarczy potwierdzić, że `ask` jest w ogóle dostępny tylko
      wewnątrz joba w tle, nie z normalnej, pierwszoplanowej tury.)
- [ ] **6.4** Job async wywołuje `report_progress(text: "...")` w trakcie
      działania (przed zakończeniem) — `wait(job_id: "<id>")` wywołany, gdy
      job jeszcze trwa, zawiera w komunikacie "still running" dokładną treść
      ostatniego zgłoszonego progresu; ta sama treść jest widoczna też po
      zakończeniu joba (progres nie jest czyszczony po `done`).
- [ ] **6.5** `resume(job_id: "<id skończonego joba async>", task: "...")` —
      dostajesz **nowy, inny** `job_id`; poll go przez `wait` jak każdy inny
      job async. Nowy task powinien odwoływać się do czegoś, co padło
      **tylko** w pierwszej turze (np. poproś model o powtórzenie liczby/
      nazwy pliku wspomnianej wyłącznie w oryginalnym zadaniu) — sprawdź, że
      wznowiony job faktycznie **widzi** ten wcześniejszy kontekst, a nie
      zaczyna od zera.
- [ ] **6.6** `resume` na `job_id`, który nie istnieje albo nigdy nie był
      wznawialny (np. synchroniczny `subagent` bez `async:true`, albo job,
      który zakończył się twardym błędem) — czysty błąd z sensownym opisem,
      bez crasha i bez zawieszenia procesu.
