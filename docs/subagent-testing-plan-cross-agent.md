# Self-test: communication and lock between agents

A supplement to `docs/subagent-testing-plan.md`, which tested `lock`/`wait`/
async `subagent` from the perspective of a **single** call thread. This file covers
what that one deliberately did not test: lock contention **between** agents
(main thread vs. background job, two background jobs against each other). See also
`docs/findings.md` (P2/P3/P4) — some of the scenarios below verify the boundaries
of those proposals, not finished functionality.

**This is not a manual test for a human.** It is a self-test: it is executed by the same
`tyci` in which you run it, on itself. You paste the prompt from the section
below as a single message, and the agent **itself** calls the successive tools
(`lock`/`unlock`/`subagent`/`wait`), itself reads the results of those calls (the content
returned by the tool — not the TUI panel, which it cannot see) and itself judges
conformance with the expectation. You only observe and optionally confirm at the
end that the report matches what you saw in the panel/modal — this is
an optional, additional verification, not a condition for running the test.

## Prompt to paste into tyci

> Run the self-test defined below on yourself. This is NOT a scenario to
> describe or plan — execute every step **for real**, with a real
> tool call, in the given order, without asking me for permission
> between steps. After each step, judge for yourself whether the result matches
> "Expected" — based on the content returned by the tool (the success `Content`
> or the `Error`), do not guess and do not assume. If a step requires waiting
> for a background job to finish, use `wait(job_id: "...", seconds:
> N)` to check it instead of guessing — that is the only way you yourself
> can see a job's status, because you have no view into the TUI panel.
>
> For steps in which you assign a task to a subagent (`subagent(...,
> task: "...")`), the `task` text must be exactly the one given below —
> the subagent (a separate model) must receive an unambiguous instruction about which
> tool call you expect from it, so do not paraphrase and do not
> shorten that text.
>
> At the end, after executing all steps from sections 6 and 7, collect
> all discrepancies into one report in a format similar to
> `docs/findings.md` (a heading per finding: P-number, short description, the exact
> calls that triggered it, expected vs. actual, suggested severity:
> bug / needs decision / no problem). If everything matched — say so
> explicitly, do not invent problems to make the report look richer. Do not save the
> report to a file yourself — show it in your response, the human will decide where to
> put it.
>
> --- SCENARIOS ---
> (paste here the content of sections 6 and 7 from `docs/subagent-testing-plan-cross-agent.md`,
> or just refer to that file if you have access to it via
> `read`)

## 6. Lock: main thread vs. background job

- [ ] **6.1** Call `lock` with `path="shared.go"`, **without** the `seconds`
      parameter. Remember the `holder` from the returned result (it will be needed in
      6.2). **Expected:** success.

      Then call `subagent` with `async=true` and `task` set
      exactly to: `"Wywołaj narzędzie lock z path=\"shared.go\" (bez seconds). Zwróć w wyniku dokładną treść komunikatu błędu, jeśli lock się nie uda."`. Wait for the job to finish via `wait(job_id, seconds: <!-- english-ok -->
      10)`. **Expected:** the job ends `done`, and its result contains a
      conflict error pointing to the `holder` from the first call.
- [ ] **6.2** Call `unlock` with `path="shared.go"` and the `holder` remembered from
      6.1. **Expected:** success, `shared.go` unlocked. (This step by itself
      does not test retry on the subagent side — scenario 7.1 serves that.)
- [ ] **6.3** Call `subagent` with `async=true` and `task` set exactly
      to: `"Wywołaj narzędzie lock z path=\"x.go\" (bez seconds). Nie wywołuj unlock. Zakończ odpowiedź krótkim tekstem 'gotowe'."`. Wait <!-- english-ok -->
      for `done` via `wait(job_id, seconds: 10)`. Immediately afterwards call
      `lock` with `path="x.go"` (without `seconds`). **Expected:** the second `lock`
      succeeds right away, without a conflict error — `x.go` must be
      automatically unlocked shortly after the job finishes (the
      `context.WithoutCancel` + auto-release mechanism, covered by the unit test
      `L-5` in `wiring_test.go` — this checks the same property end-to-end).
- [ ] **6.4** Like 6.3, but the job is to **fail**. Call `subagent` with
      `async=true` and `task` set exactly to: `"Wywołaj narzędzie lock z path=\"y.go\" (bez seconds). Następnie wywołaj narzędzie bash z komendą \"this-command-does-not-exist-xyz\". Nie wywołuj unlock."`. <!-- english-ok -->
      Wait for status `failed` via `wait(job_id, seconds: 10)`.
      Immediately afterwards call `lock` with `path="y.go"` (without `seconds`).
      **Expected:** success even though the job ended with an error — the deferred
      release also works on the error path.

## 7. Lock: two background jobs against each other

- [ ] **7.1** Call `subagent` with `async=true` and `tasks` set to
      exactly two elements:
      1. `{"task": "Wywołaj narzędzie lock z path=\"hot.go\" (bez seconds). Jeśli się uda, wywołaj wait z seconds=5. Następnie zakończ krótkim tekstem 'gotowe A', nie wywołuj unlock.", "async": true}` <!-- english-ok -->
      2. `{"task": "Wywołaj narzędzie lock z path=\"hot.go\" (bez seconds). Jeśli dostaniesz błąd konfliktu, wywołaj wait z seconds=2, a następnie spróbuj lock ponownie. Powtórz to maksymalnie 3 razy. Zakończ tekstem 'gotowe B' jeśli się udało, albo 'nie udało się B' jeśli nie po 3 próbach.", "async": true}` <!-- english-ok -->

      For both returned `job_id`s call `wait(job_id, seconds: 15)` until both
      reach status `done`. **Expected:** both `done`; the result of the first
      contains "gotowe A"; the result of the second contains "gotowe B" (not "nie udało się <!-- english-ok -->
      B" — if that appears, it means the first did not release the lock
      despite the absence of `unlock`, i.e. a bug in auto-release, not in this scenario).
- [ ] **7.2** Repeat step 7.1 (a new `subagent` call with the same
      two tasks) 3 more times in a row. **Expected:** the result is
      stable every time — always exactly one of the two gets the lock
      first (the second has a trace of a conflict error with retry in its result),
      never both "gotowe" at once without any conflict error in either of the two
      results (which would mean both got the lock simultaneously), and never
      "nie udało się B". <!-- english-ok -->
- [ ] **7.3** A three-way race for the same resource (extending 7.1 from 2 to 3).
      Call `subagent` with `async=true` and `tasks` set to exactly
      three elements, each with the same pattern as task B in 7.1 (lock →
      on conflict wait(seconds=2) and retry, max 5 times), but with a **different**
      final text for each (`"gotowe C1"`, `"gotowe C2"`, `"gotowe
      C3"`) and all on `path="triple.go"`. `wait` on all three
      `job_id`s (seconds: 20 each). **Expected:** all three `done` with the
      corresponding "gotowe Cn" (none "nie udało się"); in the content of the results of at <!-- english-ok -->
      least two of the three there must be a trace of a conflict error (because they
      could not all get the lock on the first try) — if in
      none of the three results there is a trace of a conflict, it means the test did not
      force real contention (release too fast) and it needs to be
      repeated with a longer `wait` on the winner's side.
- [ ] **7.4** No real deadlock with reverse lock order
      (`lock` is non-blocking — it returns an error immediately instead of waiting, so the
      classic two-resource deadlock should structurally not be
      possible; this test pins that). Call `subagent` with `async=true` and
      `tasks`:
      1. `{"task": "Wywołaj lock z path=\"r1.go\" (bez seconds). Jeśli się uda, wywołaj wait z seconds=3. Następnie spróbuj lock z path=\"r2.go\". Zakończ tekstem opisującym wynik obu prób lock.", "async": true}` <!-- english-ok -->
      2. `{"task": "Wywołaj lock z path=\"r2.go\" (bez seconds). Jeśli się uda, wywołaj wait z seconds=3. Następnie spróbuj lock z path=\"r1.go\". Zakończ tekstem opisującym wynik obu prób lock.", "async": true}` <!-- english-ok -->

      `wait` on both `job_id`s (seconds: 15). **Expected:** both `done` in
      a reasonable time (no hang, no timeout on `wait`) — each reports
      success on its first lock and a conflict error on the second attempt
      (because the second resource is held by the other one). This confirms that the
      system has no built-in "wait until the resource is released" mode in `lock`
      itself (that would be a source of real deadlock) — the current model
      "try, get an error, decide for yourself what to do next" is safe from this
      point of view.
- [ ] **7.5** Passing the "baton" via an explicit `unlock` (in contrast to
      7.1, where the winner does **not** call unlock and relies solely on
      auto-release after the job finishes). Call `subagent` with `async=true`
      and `tasks`:
      1. `{"task": "Wywołaj lock z path=\"queue.go\" (bez seconds). Wywołaj wait z seconds=3 (symulacja pracy). Wywołaj unlock z path=\"queue.go\" i holderem który dostałeś z lock. Zakończ tekstem 'praca A gotowa'.", "async": true}` <!-- english-ok -->
      2. `{"task": "W pętli maksymalnie 5 razy: wywołaj lock z path=\"queue.go\" (bez seconds); jeśli sukces, zakończ tekstem 'odebrano po ' + numer próby + ' próbach'; jeśli konflikt, wywołaj wait z seconds=1 i spróbuj ponownie.", "async": true}` <!-- english-ok -->

      `wait` on both `job_id`s (seconds: 15). **Expected:** task 2 never
      gets the lock on the first attempt (because task 1 holds it for at
      least ~3s), it gets it only after task 1's explicit `unlock` — i.e.
      not earlier than the attempt count corresponding to ~3s elapsing. If
      task 2 reports success on the first attempt, it means the `lock` on
      `queue.go` did not work at all in task 1 — treat this as a
      serious finding, not a trifle.
- [ ] **7.6** Visibility of `wait(job_id)` **from inside** another subagent, not
      only from the main thread (today the only channel through which one job can
      learn about the state of another). First call `subagent` with
      `async=true, task="Wywołaj wait z seconds=8, potem zakończ tekstem 'A gotowe'."` — remember the returned `job_id` (call it `<ID_A>`, <!-- english-ok -->
      substituting the real value in the step below). Immediately afterwards call
      `subagent` with `async=true` and `task` set exactly to (substituting the
      real `<ID_A>`): `"Wywołaj narzędzie wait z job_id=\"<ID_A>\" i seconds=15. Zwróć dokładną treść odpowiedzi tego wywołania jako swój wynik."`. `wait` on the second `job_id` (seconds: 20). **Expected:** the second <!-- english-ok -->
      job ends `done`, and its result contains content along the lines of "job
      finished" with the text "A gotowe" — proof that `wait` is available
      inside a subagent and sees the same shared `jobs.Registry` as the
      main thread (not a separate, per-agent instance).

## 8. Communication between subagents via the main thread

Direct subagent→subagent communication (without the main thread as intermediary)
is deliberately out of scope for this round of work ("nested agents" — skipped at
an early planning stage). The only channel available today is the main thread
reading the result of one job and **manually** pasting it into the `task` text of
the next — the scenarios below verify that this works predictably at all.

- [ ] **8.1** Produce→consume with the main thread as intermediary. Call
      `subagent` with `async=true, task="Wymyśl i zwróć jako wynik jedno losowe słowo-hasło (dowolne, jedno słowo, bez wyjaśnień)."`. Wait for <!-- english-ok -->
      `done` via `wait(job_id, seconds: 10)` and remember the exact password from the
      returned result. Then call `subagent` with `async=true, task`
      set exactly to (substituting the real password): `"Otrzymane hasło to: '<HASŁO>'. Zwróć jako wynik to samo hasło zapisane wielkimi literami."`. `wait` on the second `job_id`. **Expected:** the result of the second <!-- english-ok -->
      job is the password from the first correctly converted to capital letters —
      confirms that the main thread is able to faithfully pass the result
      of one job as input to the other (the only "communication channel"
      between subagents that exists today).
- [ ] **8.2** A subagent trying to bypass the lack of direct communication by
      calling `subagent` itself (recursion). Call `subagent` with
      `async=true, task="Wywołaj narzędzie subagent z task='cokolwiek'. Zwróć jako wynik dokładną treść błędu, jeśli to się nie uda."`. `wait` on <!-- english-ok -->
      `job_id` (seconds: 10). **Expected:** the job ends `done` (not
      `failed` — the child should handle the error and finish
      correctly), and its result contains a message about forbidden recursion.
      If the job instead hangs/times out or actually started a
      nested subagent — that is a bug (the equivalent of test `A-11` from
      `wiring_test.go`, here verified end-to-end instead of with a fake LLM).

## What is NOT tested here (deliberate gap)

The following would require mechanisms described as open in `docs/findings.md`
(P3/P4) or in the "What is NOT implemented yet" section of the main plan:

- A subagent **asking** the main thread to release a lock (a blocking `ask`) —
  today the only available strategy is `wait` + retry in a loop on the model side
  (see 7.1), there is no dedicated "lock released" notification mechanism.
- `read` warning that the path being read is currently locked by
  another agent (P3 — proposal, not implemented).
- `write`/`edit` physically enforcing the lock (P2 — a deliberately accepted
  gap, `lock` is today purely advisory/cooperative, nothing blocks
  the actual write to a locked path).
- A structured `note`/`intent` on a lock visible to other agents without
  parsing the error text (P4 — proposal).
