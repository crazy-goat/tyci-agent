# Self-test: komunikacja i lock między agentami

Uzupełnienie do `docs/subagent-testing-plan.md`, które testowało `lock`/`wait`/
async `subagent` z perspektywy **jednego** wątku wywołań. Ten plik pokrywa to,
czego tamten świadomie nie testował: kontencję locków **między** agentami
(główny wątek vs. job w tle, dwa joby w tle między sobą). Zobacz też
`docs/findings.md` (P2/P3/P4) — część scenariuszy niżej to weryfikacja granic
tych propozycji, nie gotowej funkcjonalności.

**To nie jest test manualny dla człowieka.** To self-test: wykonuje go ten sam
`tyci`, w którym go uruchamiasz, na sobie samym. Wklejasz prompt z sekcji
niżej jako jedną wiadomość, a agent **sam** wywołuje kolejne narzędzia
(`lock`/`unlock`/`subagent`/`wait`), sam odczytuje wyniki tych wywołań (treść
zwróconą przez narzędzie — nie panel TUI, którego nie widzi) i sam ocenia
zgodność z oczekiwaniem. Ty tylko obserwujesz i ewentualnie potwierdzasz na
koniec, że raport się zgadza z tym, co widziałeś w panelu/modalu — to
opcjonalna, dodatkowa weryfikacja, nie warunek wykonania testu.

## Prompt do wklejenia w tyci

> Wykonaj na sobie self-test zdefiniowany niżej. To NIE jest scenariusz do
> opisania czy zaplanowania — wykonaj każdy krok **naprawdę**, prawdziwym
> wywołaniem narzędzia, w podanej kolejności, bez pytania mnie o zgodę
> pomiędzy krokami. Po każdym kroku sam oceń, czy wynik zgadza się z
> "Oczekiwane" — na podstawie treści zwróconej przez narzędzie (`Content`
> sukcesu albo `Error`), nie zgaduj i nie zakładaj. Jeśli krok wymaga
> odczekania na zakończenie joba w tle, użyj `wait(job_id: "...", seconds:
> N)` żeby to sprawdzić zamiast zgadywać — to jedyny sposób, w jaki Ty sam
> możesz zobaczyć status joba, boa nie masz wglądu w panel TUI.
>
> Dla kroków, w których zlecasz zadanie subagentowi (`subagent(...,
> task: "...")`), treść `task` musi być dokładnie tą podaną niżej — to
> subagent (osobny model) musi dostać jednoznaczną instrukcję jakiego
> wywołania narzędzia od niego oczekujesz, więc nie parafrazuj i nie
> skracaj tej treści.
>
> Na koniec, po wykonaniu wszystkich kroków z sekcji 6 i 7, zbierz
> wszystkie niezgodności w jeden raport w formacie zbliżonym do
> `docs/findings.md` (nagłówek per finding: P-numer, krótki opis, dokładne
> wywołania które to wywołały, oczekiwane vs. rzeczywiste, sugerowana waga:
> bug / do decyzji / brak problemu). Jeśli wszystko się zgadzało — napisz to
> wprost, nie zmyślaj problemów żeby raport wyglądał bogaciej. Nie zapisuj
> raportu do pliku sam — pokaż go w odpowiedzi, human zdecyduje gdzie go
> umieścić.
>
> --- SCENARIUSZE ---
> (wklej tu treść sekcji 6 i 7 z `docs/subagent-testing-plan-cross-agent.md`,
> albo po prostu odwołaj się do tego pliku jeśli masz do niego dostęp przez
> `read`)

## 6. Lock: główny wątek vs. job w tle

- [ ] **6.1** Wywołaj `lock` z `path="shared.go"`, **bez** parametru
      `seconds`. Zapamiętaj `holder` ze zwróconego wyniku (będzie potrzebny w
      6.2). **Oczekiwane:** sukces.

      Następnie wywołaj `subagent` z `async=true` i `task` ustawionym
      dokładnie na: `"Wywołaj narzędzie lock z path=\"shared.go\" (bez
      seconds). Zwróć w wyniku dokładną treść komunikatu błędu, jeśli lock
      się nie uda."`. Poczekaj na zakończenie joba przez `wait(job_id, seconds:
      10)`. **Oczekiwane:** job kończy się `done`, a w jego wyniku jest błąd
      konfliktu wskazujący na `holder` z pierwszego wywołania.
- [ ] **6.2** Wywołaj `unlock` z `path="shared.go"` i `holder` zapamiętanym z
      6.1. **Oczekiwane:** sukces, `shared.go` odblokowane. (Ten krok sam w
      sobie nie testuje retry po stronie subagenta — do tego służy scenariusz
      7.1.)
- [ ] **6.3** Wywołaj `subagent` z `async=true` i `task` ustawionym dokładnie
      na: `"Wywołaj narzędzie lock z path=\"x.go\" (bez seconds). Nie
      wywołuj unlock. Zakończ odpowiedź krótkim tekstem 'gotowe'."`. Poczekaj
      na `done` przez `wait(job_id, seconds: 10)`. Natychmiast potem wywołaj
      `lock` z `path="x.go"` (bez `seconds`). **Oczekiwane:** drugie `lock`
      się udaje od razu, bez błędu konfliktu — `x.go` musi zostać
      automatycznie odblokowane krótko po zakończeniu joba (mechanizm
      `context.WithoutCancel` + auto-release, pokryty testem jednostkowym
      `L-5` w `wiring_test.go` — to sprawdza tę samą własność end-to-end).
- [ ] **6.4** Jak 6.3, ale job ma **zawieść**. Wywołaj `subagent` z
      `async=true` i `task` ustawionym dokładnie na: `"Wywołaj narzędzie lock
      z path=\"y.go\" (bez seconds). Następnie wywołaj narzędzie bash z
      komendą \"this-command-does-not-exist-xyz\". Nie wywołuj unlock."`.
      Poczekaj na status `failed` przez `wait(job_id, seconds: 10)`.
      Natychmiast potem wywołaj `lock` z `path="y.go"` (bez `seconds`).
      **Oczekiwane:** sukces mimo że job zakończył się błędem — deferred
      release działa też na ścieżce błędu.

## 7. Lock: dwa joby w tle między sobą

- [ ] **7.1** Wywołaj `subagent` z `async=true` i `tasks` ustawionym na
      dokładnie dwa elementy:
      1. `{"task": "Wywołaj narzędzie lock z path=\"hot.go\" (bez seconds).
         Jeśli się uda, wywołaj wait z seconds=5. Następnie zakończ krótkim
         tekstem 'gotowe A', nie wywołuj unlock.", "async": true}`
      2. `{"task": "Wywołaj narzędzie lock z path=\"hot.go\" (bez seconds).
         Jeśli dostaniesz błąd konfliktu, wywołaj wait z seconds=2, a
         następnie spróbuj lock ponownie. Powtórz to maksymalnie 3 razy.
         Zakończ tekstem 'gotowe B' jeśli się udało, albo 'nie udało się B'
         jeśli nie po 3 próbach.", "async": true}`

      Dla obu zwróconych `job_id` wywołaj `wait(job_id, seconds: 15)` aż oba
      dojdą do statusu `done`. **Oczekiwane:** oba `done`; wynik pierwszego
      zawiera "gotowe A"; wynik drugiego zawiera "gotowe B" (nie "nie udało
      się B" — jeśli to się pojawi, to znaczy że pierwszy nie zwolnił locka
      mimo braku `unlock`, czyli bug w auto-release, a nie w tym scenariuszu).
- [ ] **7.2** Powtórz krok 7.1 (nowe wywołanie `subagent` z tymi samymi
      dwoma zadaniami) jeszcze 3 razy pod rząd. **Oczekiwane:** wynik
      stabilny za każdym razem — zawsze dokładnie jeden z dwóch dostaje lock
      jako pierwszy (drugi ma w swoim wyniku ślad błędu konfliktu z retry),
      nigdy oba naraz "gotowe" bez żadnego błędu konfliktu w żadnym z dwóch
      wyników (co by znaczyło, że oba dostały lock jednocześnie), i nigdy
      "nie udało się B".
- [ ] **7.3** Trójstronny wyścig o ten sam zasób (rozszerzenie 7.1 z 2 na 3).
      Wywołaj `subagent` z `async=true` i `tasks` ustawionym na dokładnie
      trzy elementy, każdy z tym samym wzorcem co zadanie B w 7.1 (lock →
      jeśli konflikt to wait(seconds=2) i retry, max 5 razy), ale z **innym**
      tekstem końcowym dla każdego (`"gotowe C1"`, `"gotowe C2"`, `"gotowe
      C3"`) i wszystkie na `path="triple.go"`. `wait` na wszystkie trzy
      `job_id` (seconds: 20 każdy). **Oczekiwane:** wszystkie trzy `done` z
      odpowiednim "gotowe Cn" (żaden "nie udało się"); w treści wyników co
      najmniej dwóch z trzech musi wystąpić ślad błędu konfliktu (bo nie
      mogły wszystkie trzy dostać locka za pierwszym razem) — jeśli w
      żadnym z trzech wyników nie ma śladu konfliktu, to znaczy że test nie
      wymusił realnej rywalizacji (zbyt szybkie zwolnienie) i trzeba
      powtórzyć z dłuższym `wait` po stronie zwycięzcy.
- [ ] **7.4** Brak prawdziwego deadlocku przy odwrotnej kolejności locków
      (`lock` jest nieblokujący — zwraca błąd od razu zamiast czekać, więc
      klasyczny deadlock dwóch zasobów strukturalnie nie powinien być
      możliwy; ten test to pinuje). Wywołaj `subagent` z `async=true` i
      `tasks`:
      1. `{"task": "Wywołaj lock z path=\"r1.go\" (bez seconds). Jeśli się
         uda, wywołaj wait z seconds=3. Następnie spróbuj lock z
         path=\"r2.go\". Zakończ tekstem opisującym wynik obu prób lock.",
         "async": true}`
      2. `{"task": "Wywołaj lock z path=\"r2.go\" (bez seconds). Jeśli się
         uda, wywołaj wait z seconds=3. Następnie spróbuj lock z
         path=\"r1.go\". Zakończ tekstem opisującym wynik obu prób lock.",
         "async": true}`

      `wait` na oba `job_id` (seconds: 15). **Oczekiwane:** oba `done` w
      rozsądnym czasie (nie hang, nie timeout na `wait`) — każdy zgłasza
      sukces na swoim pierwszym locku i błąd konfliktu na drugiej próbie
      (bo drugi zasób jest trzymany przez tego drugiego). To potwierdza że
      system nie ma trybu "poczekaj aż zasób się zwolni" wbudowanego w sam
      `lock` (byłoby to źródłem prawdziwego deadlocku) — obecny model
      "spróbuj, dostań błąd, sam zdecyduj co dalej" jest bezpieczny z tego
      punktu widzenia.
- [ ] **7.5** Przekazanie "pałeczki" przez jawny `unlock` (w kontraście do
      7.1, gdzie zwycięzca **nie** wywołuje unlock i polega wyłącznie na
      auto-release po zakończeniu joba). Wywołaj `subagent` z `async=true`
      i `tasks`:
      1. `{"task": "Wywołaj lock z path=\"queue.go\" (bez seconds). Wywołaj
         wait z seconds=3 (symulacja pracy). Wywołaj unlock z
         path=\"queue.go\" i holderem który dostałeś z lock. Zakończ
         tekstem 'praca A gotowa'.", "async": true}`
      2. `{"task": "W pętli maksymalnie 5 razy: wywołaj lock z
         path=\"queue.go\" (bez seconds); jeśli sukces, zakończ tekstem
         'odebrano po ' + numer próby + ' próbach'; jeśli konflikt, wywołaj
         wait z seconds=1 i spróbuj ponownie.", "async": true}`

      `wait` na oba `job_id` (seconds: 15). **Oczekiwane:** zadanie 2 nigdy
      nie dostaje locka przy pierwszej próbie (bo zadanie 1 trzyma go co
      najmniej ~3s), dostaje go dopiero po jawnym `unlock` zadania 1 — czyli
      nie wcześniej niż licznik prób odpowiadający upływowi ~3s. Jeśli
      zadanie 2 zgłosi sukces przy pierwszej próbie, to znaczy że `lock` na
      `queue.go` w ogóle nie zadziałał w zadaniu 1 — potraktuj to jako
      poważne znalezisko, nie drobiazg.
- [ ] **7.6** Widoczność `wait(job_id)` **z wnętrza** innego subagenta, nie
      tylko z głównego wątku (dziś jedyny kanał, w jaki jeden job może
      dowiedzieć się o stanie drugiego). Najpierw wywołaj `subagent` z
      `async=true, task="Wywołaj wait z seconds=8, potem zakończ tekstem
      'A gotowe'."` — zapamiętaj zwrócony `job_id` (nazwij go `<ID_A>`
      podstawiając realną wartość w kroku niżej). Natychmiast potem wywołaj
      `subagent` z `async=true` i `task` ustawionym dokładnie na (podstawiając
      prawdziwe `<ID_A>`): `"Wywołaj narzędzie wait z job_id=\"<ID_A>\" i
      seconds=15. Zwróć dokładną treść odpowiedzi tego wywołania jako swój
      wynik."`. `wait` na drugi `job_id` (seconds: 20). **Oczekiwane:** drugi
      job kończy się `done`, a jego wynik zawiera treść w stylu "job
      finished" z tekstem "A gotowe" — dowód, że `wait` jest dostępny
      wewnątrz subagenta i widzi ten sam, współdzielony `jobs.Registry` co
      główny wątek (nie osobną, per-agentową instancję).

## 8. Komunikacja między subagentami przez główny wątek

Bezpośrednia komunikacja subagent→subagent (bez pośrednictwa głównego wątku)
jest świadomie poza zakresem tej rundy prac ("nested agents" — pominięte na
wczesnym etapie planowania). Jedyny dziś dostępny kanał to główny wątek
odczytujący wynik jednego joba i **ręcznie** wklejający go do treści `task`
kolejnego — poniższe scenariusze weryfikują, że to w ogóle działa
przewidywalnie.

- [ ] **8.1** Produkuj→konsumuj przez główny wątek jako pośrednika. Wywołaj
      `subagent` z `async=true, task="Wymyśl i zwróć jako wynik jedno losowe
      słowo-hasło (dowolne, jedno słowo, bez wyjaśnień)."`. Poczekaj na
      `done` przez `wait(job_id, seconds: 10)` i zapamiętaj dokładne hasło ze
      zwróconego wyniku. Następnie wywołaj `subagent` z `async=true, task`
      ustawionym dokładnie na (podstawiając prawdziwe hasło): `"Otrzymane
      hasło to: '<HASŁO>'. Zwróć jako wynik to samo hasło zapisane wielkimi
      literami."`. `wait` na drugi `job_id`. **Oczekiwane:** wynik drugiego
      joba to poprawnie zamienione na wielkie litery hasło z pierwszego —
      potwierdza, że główny wątek jest w stanie wiernie przekazać wynik
      jednego joba jako wejście drugiego (jedyny dziś istniejący "kanał
      komunikacji" między subagentami).
- [ ] **8.2** Subagent próbujący ominąć brak bezpośredniej komunikacji przez
      wywołanie `subagent` samego siebie (rekurencja). Wywołaj `subagent` z
      `async=true, task="Wywołaj narzędzie subagent z task='cokolwiek'. Zwróć
      jako wynik dokładną treść błędu, jeśli to się nie uda."`. `wait` na
      `job_id` (seconds: 10). **Oczekiwane:** job kończy się `done` (nie
      `failed` — to child powinien obsłużyć błąd i zakończyć się
      poprawnie), a jego wynik zawiera komunikat o zabronionej rekurencji.
      Jeśli job zamiast tego wisi/timeoutuje albo faktycznie wystartował
      zagnieżdżonego subagenta — to jest bug (odpowiednik testu `A-11` z
      `wiring_test.go`, tu weryfikowany end-to-end zamiast fakeowym LLM).

## Co NIE jest tu testowane (świadomy brak)

Poniższe wymagałyby mechanizmów opisanych jako otwarte w `docs/findings.md`
(P3/P4) lub w sekcji "Co NIE jest jeszcze zaimplementowane" głównego planu:

- Subagent **proszący** główny wątek o zwolnienie locka (blokujące `ask`) —
  dziś jedyna dostępna strategia to `wait` + retry w pętli po stronie modelu
  (patrz 7.1), nie ma dedykowanego mechanizmu powiadomienia "lock released".
- `read` ostrzegający, że czytana ścieżka jest aktualnie zalokowana przez
  innego agenta (P3 — propozycja, niezaimplementowana).
- `write`/`edit` fizycznie egzekwujące lock (P2 — świadomie zaakceptowany
  brak, `lock` jest dziś czysto advisory/kooperacyjny, nic nie blokuje
  faktycznego zapisu na zablokowanej ścieżce).
- Strukturalny `note`/`intent` przy locku widoczny dla innych agentów bez
  parsowania treści błędu (P4 — propozycja).
