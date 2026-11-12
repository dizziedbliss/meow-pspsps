# HANDOFF: VeryJudgyCat for Automathon 2.0

**Update:** This is the earlier Claude handoff. The current event state and follow-up test results are in `EVENT-READINESS.md`; use `HANDBOOK.md` for event-day steps.

Written 2026-11-12 about 05:55 IST by Claude, for whoever continues (Codex or a new Claude session).
Read this file top to bottom once. The project rules are in `CLAUDE.md`; the commands are in `README.md`.

**The event is today.** Round 1 (Hostel Mess Feedback) starts around **08:30 IST**, Round 2 (Emergency Response)
follows. About 2 hours per round, 86 teams, 3 scored attempts per round (best counts), unlimited free samples.

## 1. What this is

A LeetCode-style judge for n8n workflows. A team exports its n8n workflow and uploads it to the site. The judge
imports it into a fresh n8n 2.47.4 container, calls its webhooks with generated test inputs, grades the replies
and shows verdicts. Organisers get marks, offline scores, a leaderboard (`/board`), a CSV and an incident list.
One Go binary (`cmd/judge`), SQLite, server-rendered pages. Names stay `meow-pspsps` / `meow-judge` in code,
Docker and the Go module; the display name is **VeryJudgyCat**, the event **Automathon 2.0**.

## 2. Status at a glance

| Area | State | Evidence |
|---|---|---|
| Problem packs `problems/1-mess`, `problems/2-emergency` | Done, verified on real n8n | Mess ideal 18/18 (3 runs). Emergency ideal 19/19 (several runs, latest with the final 1/2/5 minute deadlines). Broken workflows fail the intended tests |
| Runner, grader, platform (attempts, refunds, marks, offline scores, `/board`, CSV, incidents) | Done, verified end to end through the website with a real worker | sample 2/2, attempt 18/18 = 36 marks, wrong workflow 16/18, refund and incident on a forced judge-side failure, board and CSV totals agree |
| Hidden tests hidden from teams | Done and tested | teams see only the samples plus "Hidden tests: X of Y passed" |
| Grader: provider chain, many Gemini keys, cache, pattern-first checks | Done, tested | `~/.config/meow-judge/grader.json` + `gemini-keys.txt` |
| Worker PC script `deploy/veryjudgycat.sh` (install, status, uninstall) | Verified on a real Ubuntu 24.04 (throwaway WSL distro, now deleted) | later small fixes were only dry-run |
| Central start script `deploy/start-central.sh` | Dry-run tested, and used for real with `--no-tunnel` | the tunnel was blocked by the permission system |
| PDFs v2 and the announcement | Drafted, not yet approved by Max | see section 6 |
| Robustness test, load test, code review | **Were running when this was written** | section 7 |
| Git | **Nothing is committed.** Branch `feat/mess-feedback`, about 57 changed or new files | `git status` |

## 3. Where things are

- `cmd/judge/`: `main.go` (subcommands), `serve.go` (serve, worker, teams, grader config), `tools.go` (`check`), `mockcmd.go`.
- `internal/workflow/`: parse an export, map the required webhooks (`entries.go`), `PrepareEntries`, feature report (`features.go`), Sheets headers from a team's column mapping (`sheets.go`), node pre-check against `internal/catalog`.
- `internal/runner/`: Docker pool; `isolated.go` runs each test on its own workflow copy with its own simulated accounts; `refs.go` (`<<stepN.path>>` between steps); `lastnode.go` ("respond immediately" webhooks); `telegram.go`.
- `internal/mock/`: fake Telegram, Google Calendar, Google Sheets, Gmail, with a per-start admin bearer token.
- `internal/grade/`: verdicts, checks (`expect.json|mock|alerts|rubric|rubric_fast`), `chain.go` (grader failover, cache, cooldown), `hints.go` (plain hints for common n8n errors).
- `internal/problem/`: problem loading and rendering, `starter.go` (paste-able starter workflow), `NewJob`, `RenderAt`.
- `internal/server/`: team and admin pages, queue, attempts and refunds, board, CSV, incidents, worker API (`/api/worker/claim|result|ping`).
- `web/`: templates, `static/app.js`, `style.css`.
- `problems/1-mess/`, `problems/2-emergency/`: `problem.yaml`, `statement.md`, `ideal.json`, `tests/`, broken workflows. `0-dummy` and `1-booky` are older and not used at the event.
- `deploy/`: `make-bundle.sh`, `veryjudgycat.sh`, `start-central.sh`.
- `handoff/reports/`: the final reports of the three background checks (section 7).

## 4. Secrets and local files (never commit)

In `~/.config/meow-judge/`: `admin.pw` (organiser password), `worker.token`, `grader.json` (grader providers),
`gemini-keys.txt` (one Gemini key per line, read at server start), `judge-llm.key` (the key the ideals use),
`teams-names.txt` (86 teams, tab separated `AA01<TAB>name`; duplicate names are different teams).
In the repo, git-ignored: `event.db*`, `teams.csv` (logins and passwords), `logs/`, `dist/`, `bin/`, `.playwright-mcp/`.
`judge.db*` is old testing data; ignore it.

## 5. How to run things

```bash
# checks (all must be clean)
gofmt -l . && go vet ./... && go test ./...
CGO_ENABLED=0 go build -o bin/judge ./cmd/judge           # static, bake copies it into the mock image

# prove an ideal on real n8n (stops other judge processes' containers; take the lock)
JUDGE_LLM_CONFIG=$HOME/.config/meow-judge/grader.json flock /tmp/judge-check.lock \
  bin/judge check --problem problems/1-mess --workflow problems/1-mess/ideal.json --times 3 \
  --cred-file googlePalmApi.apiKey=$HOME/.config/meow-judge/judge-llm.key
# same for problems/2-emergency (about 100 s per run); expected 18/18 and 19/19

# central site (local only; the tunnel needs Max's approval, see section 6)
deploy/start-central.sh --pool 2 --no-tunnel               # add --make-teams the first time
kill $(cat logs/central.pid)                                # stop it

# worker bundle for lab PCs, then on each PC (Ubuntu, sudo)
deploy/make-bundle.sh                                       # writes dist/worker-bundle/
sudo ./veryjudgycat.sh install --central <https://site> --token "$(cat ~/.config/meow-judge/worker.token)" --pool 2
sudo ./veryjudgycat.sh status
sudo ./veryjudgycat.sh uninstall                            # after the event; removes Docker only if the script installed it
```

Admin: `<site>/admin/login` (password in `admin.pw`). Teams: `<site>/` with code and password from `teams.csv`.
Round ids are `1=mess,2=2-emergency` (set in `start-central.sh`).

## 6. What is left, in order

1. **Collect the three background reports** in `handoff/reports/` (section 7), fix every BLOCKER and SHOULD FIX, add a test per fix, rerun `gofmt`, `go vet`, `go test`.
2. **Rerun both ideals** with the final binary (commands above): expect 18/18 and 19/19. Also rerun once with a broken workflow to confirm failures still show.
3. **Rebuild the worker bundle** (`deploy/make-bundle.sh`); the one in `dist/` was built before the last fixes.
4. **Reset event data.** `event.db` holds test attempts (2 scored attempts and 2 samples on `AA01`). Do it with the site stopped:
   `kill $(cat logs/central.pid); mv event.db event.test.db; mv teams.csv teams.test.csv; deploy/start-central.sh --make-teams --pool 2 --no-tunnel`
   This creates fresh logins and passwords: print `teams.csv` for the organisers.
5. **Tunnel: running since 05:51** (Max started it himself). Public address: `https://cost-christina-profession-nice.trycloudflare.com` (a quick tunnel: the address changes if cloudflared restarts, which would break workers installed with it; a named tunnel on a Cloudflare domain is stable, and Max has not said whether he has one). Never stop or restart cloudflared casually. The permission system blocks Claude from opening tunnels, so Max did it with `deploy/start-central.sh --pool 2`; do not start a second tunnel (use `--no-tunnel` when restarting the site). Cloudflared is at `/mnt/c/Program Files (x86)/cloudflared/cloudflared.exe`.
   **Live state at 05:52:** the real central is up on `127.0.0.1:8080` with `--pool 0` (no worker pool, so it does not touch docker while the robustness and load tests use it). Runs submitted now would queue and not execute. When the tests are done, restore the pool: `kill $(cat logs/central.pid); deploy/start-central.sh --pool 2 --no-tunnel`. Starting a pool wipes every `meow.judge` container and recreates the shared `meow-judge-mock`, which breaks any test running at the time.
6. **Lab PCs** (Ubuntu with sudo, no Docker): trial the install on one PC first, then the rest. Pool 2 suits a 6 GB PC; confirm with the load test report. Each PC runs the `install` command above with the final tunnel address.
7. **In the admin page** (Max): open Round 1, set marks per test (xx, yy, zz still unknown) and the max per round, choose which rounds `/board` shows, enter offline scores later.
8. **Max still has to approve the PDFs** (`C:\Users\adity\Documents\VeryJudgyCat_Round1_Mess_v2.pdf` and `..._Round2_Emergency_v2.pdf`; the originals in Downloads are untouched). Each keeps the original page 1 and adds a plain build contract after it (an input and output table per entry point, rules in full sentences, the Emergency contact table). Two small corrections are on Emergency page 1 only (the progress line gets IN_PROGRESS, and the non-emergency note names the Not an Emergency severity). Open questions for Max: the severity wording on page 2 refers to a table that has no Not an Emergency row, `updated_by` is shown as optional, and the rating `4` or `"4"` rule. The one-minute announcement script is `handoff/announcement.md` (it uses `<SITE>` as a placeholder).
9. **Between rounds:** more Gemini keys can be added to `gemini-keys.txt` (different Google projects; each has its own free limit), but they are read only at server start, so restart between rounds, never mid-round (a restart fails queued runs).
10. **After the event:** `uninstall` on every PC, rotate the Gemini keys, delete `teams.csv`.
11. **Commit** when the above is clean: `git status`, never commit `bin/`, `dist/`, `*.db`, `teams.csv`, `logs/`, `.playwright-mcp/`; branch `feat/mess-feedback`; no force-push; review the diff for secrets first (`grep -rn "AIza\|AQ\." .` must find nothing real).

## 7. Work that was in flight when this was written

Three background agents were started by Claude. Their output goes to `handoff/reports/` when they finish:

- `robustness-test.md`: about 40 deliberately broken or hostile workflows and platform abuse cases (bad uploads, infinite loops, memory bombs, non-JSON replies, calls to internal addresses, double clicks, killed workers). It stops the real site while it runs and restarts it at the end.
- `load-test.md`: four waves (20 samples, 12 Mess attempts, 6 Emergency attempts, a mixed wave) with memory and queue timings and a recommended pool size per 6 GB PC. It waits until the robustness test is done, then stops and restarts the real site. It uses a local stand-in for Gemini, so it does not measure the free-tier limits of the real keys.
- `code-review.md`: a read-only ranked review of the whole working tree.

If a report file is missing, that agent did not finish: rerun that check yourself. Before you start, make sure the site on port 8080 is in the state you want (`curl localhost:8080/healthz`), and that no `judge serve`, `judge check` or `judge worker` process from those agents is still running (`pgrep -af "judge (serve|check|worker)"`). Those agents hold the docker containers while they work.

## 8. Decisions already made (do not re-ask Max)

- A required webhook missing from an upload rejects it (Invalid Workflow, with the team's own webhook paths listed).
- Teams may use any AI provider and model; statements are provider-neutral. An unavailable model gives the verdict **Model Unavailable** and counts as an attempt. No substitute-model fallback.
- Statements and PDFs hold only must-know text (paths, input and output fields, graded rules). No behind-the-scenes detail, no test hints.
- Hidden tests show only "X of Y passed". Samples show everything. Admin sees everything and run data is kept broad.
- Judge-side failures refund the attempt and create an incident. A dead grader is a judge-side failure, never a zero.
- Emergency escalation deadlines are Critical 1 min, High 2, Medium 5. Alert contacts are fixed (the table in the statement) because the judge cannot see a team's own phone.
- Grader: Gemini `gemini-3.1-flash-lite`, many keys in sequence. Ollama is parked (the 9B model runs out of memory in this 7.7 GB WSL); Mistral's free plan is gone.
- No Gemini billing for now.

## 9. Gotchas

- One judge process with containers per machine. `serve --local-pool`, `worker` and `check` remove every `meow.judge` container when they start or stop. Take `flock /tmp/judge-check.lock` for checks; stopping one judge process can kill another's containers (this broke one check run).
- Never install or run Docker in a second WSL distro on this machine: distros share one network and the second daemon flushed the judge network's forwarding rules (containers lost internet). Repair that happened: remove containers and the `meow-judge-net` network, recreate with `docker network create --subnet 172.30.99.0/24 --label meow.judge.net=1 meow-judge-net`.
- `pkill -f` matches its own shell; use a PID or the bracket trick. Max's login shell is fish: wrap commands for him in `bash -c '...'`.
- The fake Google Sheets starts blank. A fixed-column Sheets append used to fail on it ("Column names were updated"); the judge now seeds headers from a team's own column mapping (`internal/workflow/sheets.go`). Reading rows is the operation `read` (not `getAll`).
- Gemini free tier: 2.5 models are retired, 3.8-flash and flash-latest hang, `gemini-3.1-flash-lite` gives 15 requests per minute and 500 per day per project.
- Quick Cloudflare tunnels change address on restart.
- This laptop's WSL has 7.7 GB RAM; each idle judge container uses about 400 MB, so pool 2 here.

## 10. Known issues accepted for this event

No egress filtering (a workflow can reach the college network); one shared worker token; team keys pass through the central server's memory only; desktop-only pages; no appeals process; AI-dependent tests can vary with a team's own model; a webhook that sends an empty reply still fails; cosmetic page glitches (a sliver of text under the wordmark when the statement scrolls, clipped long code lines in statements); the Gemini key used for testing was printed once in agent tool output, so rotate it after the event.

## 11. If something breaks during the event

- Site restarts: queued and running runs are marked failed ("please run again") because keys live only in memory; scores are safe in `event.db`.
- A worker dies: its runs fail after 12 minutes and are refunded; restart it with `sudo systemctl restart veryjudgycat-worker` on the PC.
- A team reports a strange failure: look at the admin run page (every step, the simulator's snapshot, the grader provider and reasoning) and the incident list.
- Check one workflow by hand: `bin/judge check --problem problems/<p> --workflow file.json -v` (stop the site first, or the containers clash).
