# Automathon 2.0 - event readiness

Updated 12 November 2026, after the event database reset and live smoke test. This is an operational record, not a guarantee that every team, provider or college computer will work. Do not publish the private repository: it includes hidden tests and the organiser-only manual.

## Current event state

- The central site is running with two local worker slots and a Cloudflare quick tunnel. The current public address is in the running tunnel log and may change when the tunnel restarts.
- `event.db` was reset after a backup. It has 86 participant accounts plus the private `PPOOL` practice account. The only run created after reset was a PPOOL sample used for the live check. Each real team retains three scored attempts per round. Round 1 is pending until an organiser opens it.
- The current `teams.csv` is the fresh participant login list. It is private, mode 600, and must not be committed or shared wholesale. Old test data and old logins were moved to `logs/event-backup-before-reset-20261112-063442/`.
- `PPOOL` can preview the next pending round and run unlimited sequential samples and attempts. It is excluded from the public leaderboard and scoreboard CSV. Keep its password private.
- Event-day procedures, worker installation and troubleshooting are in `HANDBOOK.md`. The participant guide is linked from the site. Printable statements and an organiser-only manual of all 37 test cases are under `handoff/event-materials/`.

## Tests actually performed

| Check | Result and scope |
|---|---|
| Automated code checks | `gofmt`, `go vet ./...`, `go test ./...`, and `go test -race ./...` passed on the follow-up build. Unit coverage includes practice-account limits/leaderboard exclusion, round reopening, malformed grader replies, webhook execution timeout and the participant-guide route. |
| Mess reference | Three runs of the reference on n8n passed 18/18 each; one further run on the runner after the timeout fix passed 18/18. These used a real model API and simulated external services. |
| Emergency reference | Three runs of the reference on n8n passed 19/19 each, with the 1/2/5-minute escalation rules and the runner timeout fix. These used a real model API and simulated external services. |
| Negative submission | A deliberately incorrect Mess workflow scored 1/18: the judge caught its mistakes. |
| Live site after reset | Ordinary team login succeeded and did not expose a pending round. PPOOL login succeeded, saw the pending round, and reported unlimited state. Its real website sample run passed 2/2; the participant PDF downloaded. The HTTPS tunnel health and guide URLs returned 200. |
| Concurrent sample wave | 20 simultaneous sample submissions on two local slots: all 20 finished 2/2, no infrastructure errors; total drain 229 s, max queue wait 201 s. |
| Concurrent Mess wave | 12 simultaneous scored submissions on two slots: all completed 18/18, no infrastructure errors; total drain 327 s. |
| Concurrent Emergency wave | Six submissions on two slots completed with no infrastructure errors, drain 348 s. The deliberately simplified model stand-in answered only 8/19 checks; these are load-test results, not the real-model reference scores above. |
| Mixed-round wave | 19 submissions completed with no infrastructure errors on two slots; drain 455 s, max queue wait 402 s. The stand-in model produced the same limited Emergency results. |
| Worker bundle | The final static worker binary and two saved images are in `dist/worker-bundle/`; their SHA-256 checksums pass. Installer and uninstaller syntax/dry-runs passed. The previous session tested an earlier version on an Ubuntu 24.04 WSL installation; the final changes have not been tested on a college PC. |
| PDFs | Both participant statements and the participant guide were rendered and visually inspected. The organiser manual was generated from all 18 Mess and 19 Emergency definitions and checked for all 37 case IDs. Its inputs are one concrete set of generated values; the live judge randomises them. |

The load checks used a local stand-in for the teams' model calls to avoid rate limits, and ran before the last small site/account changes. They do not prove behavior for 86 teams at once, actual provider quotas, college Wi-Fi, or real remote workers. The previous agent's hostile-workflow test and automated security review stopped early; only its partial observations and this focused follow-up review are available in `handoff/reports/`.

## How reliable is it?

The reference flows and a live sample have passed, and two local slots handled the tested bursts without infrastructure failures. That is useful evidence for a controlled event, not a production reliability guarantee. With just two slots, 19 mixed runs took 7 minutes 35 seconds to drain; use additional college workers and expect queues. The central laptop, network, tunnel and grader are single points of failure. During testing, launching new WSL commands intermittently timed out even while the site continued to respond; keep a close eye on the laptop and have the printed manual ready.

## What it does not do / important limits

- For these event problems, Telegram, Gmail and Google Sheets are **simulated**. The workflows execute in real n8n containers and model API calls are real, but actual external accounts are not tested, no real messages are delivered, and no real spreadsheet is updated. It does not prove OAuth integration works.
- Submitted workflows can reach the worker's network and host bridge; egress isolation has not been implemented. Use dedicated workers and avoid sensitive systems on their network. This is not a security-hardened multi-tenant service.
- Real AI keys can be rejected, rate-limited, exhausted or have unavailable models; the small load test did not measure the free-tier limit. Model judgments can vary. Judge-side failures and persistent rate limits refund attempts; investigate incidents and use a consistent manual fallback when needed.
- The final worker installer has not been verified on the actual college PCs. Test one first and check its connection on the admin page. The repository is private, and a clone does not include the 352 MB worker bundle, private keys, or event database.
- The quick-tunnel address is not stable. If it changes, update the URL on all workers and notify participants. A named tunnel/domain would be more stable but is not set up here.
- There is no high availability, comprehensive abuse protection, complete security audit or real 86-team stress test. Claude's automated security review failed with HTTP 429; its local log is at `/home/dizziedbliss/.claude/security/log.txt`.
- Mark weights and maximums are deliberately left for the organiser to set on the admin page at event time. Do not forget to open the correct round. A central restart interrupts unfinished runs; they are refunded, while completed scores remain in `event.db`.
