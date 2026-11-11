# Project audit — 11 November 2026

Scope: repository code and Booky configuration, with one live Booky run. This is a risk-focused review, not a proof of security or a full event-scale load test. Findings are unremediated.

## Findings

1. **High — grading credentials can be sent to the wrong provider.** `cmd/judge/serve.go:42-51` infers Gemini from the key prefix; otherwise `internal/grade/judge.go` defaults to OpenAI. The supplied Gemini key did not match the prefix. During this audit the grader sent it in an Authorization header to OpenAI, which returned 401. Rotate that key. Require an explicit provider or endpoint, validate configuration before runs, and do not infer a provider from secret syntax.
2. **High — the simulator admin API is reachable by submitted workflows.** `cmd/judge/mockcmd.go:37` listens on all interfaces inside the shared Docker network, and `internal/mock/mock.go:98` accepts tenant changes without authentication. A workflow with its own simulated token can address that service and change its test state; broader network access is also acknowledged in `CLAUDE.md`. Isolate the admin interface from contestants and authenticate it; block access from workflows to host and private networks.
3. **High — rubric outages produce misleading scores.** `internal/grade/grade.go:193-197` treats a missing judge as a passing check and a judge API error as a wrong answer. In the live run two plausible replies were marked wrong after the grader received 401. Treat missing/unavailable grading as infrastructure failure and prevent final scores until retried.
4. **High — Booky ideal workflow can book a past date.** The live run created an event for 23 October 2026 when the test date was 11 November 2026. Inspect its date handling and add deterministic guardrails; do not treat the ideal workflow as validated.
5. **High — free-tier quota prevents reliable Booky grading.** Eight of 14 live tests returned Key Rate-Limited despite parallelism of two. Use a suitable quota and backoff/rate control, then repeat full end-to-end checks. The run's 3/14 score is not a meaningful measure of workflow quality.
6. **Medium — partial multi-step tests can be accepted.** `internal/grade/grade.go:64-74,129` verifies only that at least one step exists and grades the returned steps, without requiring every expected step. A truncated result can pass without evaluating the follow-up. Require the expected step count and report missing steps as incomplete infrastructure results.
7. **Medium — numeric checks accept non-numbers in some cases.** `internal/grade/grade.go:230-233,266-270` compares NaN from `toNum` with bounds/tolerance; those comparisons are false, allowing invalid values to pass. Reject failed conversions explicitly.
8. **Medium — Telegram update ID is modified concurrently.** `internal/runner/telegram.go:202-210` increments a package global from parallel test goroutines without synchronization. IDs can race or repeat. Use an atomic counter and exercise parallel paths under the race detector; the existing race test suite does not execute this path.
9. **Medium — active runs can be expired based on queue age.** `internal/server/store.go:313-315` uses creation time for queued, running and grading runs, although `internal/server/server.go:128` describes a timeout after a worker takes a run. A run that waited in the queue can be marked failed shortly after starting, and a later result can overwrite stale status. Track each state separately and use conditional state transitions.
10. **Medium — sessions do not expire server-side.** `internal/server/store.go:149-153` accepts any stored token indefinitely; the cookie's 24-hour MaxAge is only client-side. Expire and clean up sessions server-side.
11. **Medium — round and run checks are not atomic.** `internal/server/team.go:241-260` checks the open round, submission, and in-flight status separately from insertion. Concurrent requests or a round closure can bypass these rules. Use transactional constraints and state checks.

## Verification and limits

- `go test ./...`, `go vet ./...`, and `go test -race ./...` passed. The runner and server have no package tests, so these checks do not cover their important integration paths.
- One live Booky run: 3 Accepted, 3 Wrong Answer (two grader 401s, one actual past-date booking), and 8 Key Rate-Limited. The rubric grader used the wrong endpoint as described above. No reliable aggregate score is available.
- Two further full runs were not attempted because the first exposed the wrong-provider credential routing and quota exhaustion; repeating it would not validate stability. No full-scale concurrency, adversarial penetration, or additional workflow testing was performed.
- The site was already stopped and remains stopped. The previous session's temporary database was outside the repository, so it was not modified.

## Follow-up

The Booky behavior-test update now treats missing or unavailable rubric grading as a judge-side failure, rejects incomplete multi-step results, and requires an explicit grader endpoint for keys whose provider is not recognized. Other findings above remain open.
