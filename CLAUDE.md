# VeryJudgyCat: n8n workflow judge for Automathon 2.0

LeetCode-style judge for the hackathon "Automathon 2.0" (app name: VeryJudgyCat; repo, Go module, binary and Docker names stay `meow-pspsps` / `meow-judge`): teams upload an exported n8n workflow, the judge runs it in a
fresh n8n container against hidden tests, and shows per-test verdicts. Organisers get attempts,
scores and final submissions. Personal/college stage (not public): keep it light, no production
hardening.

## Commands

- Build: `CGO_ENABLED=0 go build -o bin/judge ./cmd/judge` (must be static: `bake` copies it into the scratch mock image)
- Check: `gofmt -l . && go vet ./... && go test ./...`
- Bake the judge n8n image on a machine (needs Docker): `bin/judge bake`
  (organisers only: `--catalog internal/catalog/catalog.json` regenerates the embedded node catalog;
  rebuild the binary after)
- Run a workflow against a problem locally: `bin/judge check --problem problems/0-dummy --workflow problems/0-dummy/ideal.json [--times 3] [-v] [--cred openAiApi.apiKey=...]`
- Central site: `JUDGE_ADMIN_PASSWORD=... JUDGE_WORKER_TOKEN=... bin/judge serve --rounds 1=p1,2=p2,3=p3 [--local-pool N]`
  (rubric grader: `JUDGE_LLM_CONFIG=file.json`, a list of providers tried in order with explicit `base`, `model`, `key_file`, `gap_ms`; or one provider via `JUDGE_LLM_BASE` + `JUDGE_LLM_MODEL` + `JUDGE_LLM_KEY[_FILE]`. Nothing is guessed from a key prefix. Mine: `~/.config/meow-judge/grader.json`, with `keys_file` pointing at `gemini-keys.txt`: one Gemini key per line, one provider per key, sticky with a 1-minute cooldown on a failing key)
- Central server for the event: `deploy/start-central.sh [--make-teams] [--pool N] [--tunnel NAME]` (fresh `event.db`, secrets in `~/.config/meow-judge`, Cloudflare tunnel).
- Worker PCs without Docker: `deploy/make-bundle.sh`, then `sudo ./veryjudgycat.sh install|status|uninstall` from the bundle on each (Debian/Ubuntu; see README).
- Worker on another machine: `bin/judge worker --central https://<site> --token <worker token> --pool N`
- Team logins: `bin/judge teams --db event.db --names names.txt --out teams.csv` (a line `AA07<tab>Team name` keeps that code; no code = generated `T001`). The real list is `~/.config/meow-judge/teams-names.txt` (86 teams, AA01 to AA86; duplicate names are different teams; never commit it).

## Architecture

- One Go binary (`cmd/judge`), SQLite (`modernc.org/sqlite`, pure Go), server-rendered HTML plus
  `web/static/app.js`, all embedded with `go:embed`.
- `internal/workflow`: parse an export, find the single Webhook/Chat Trigger (or map a problem's `entries:`), pre-check node types
  and versions against `internal/catalog`, detect needed credentials (incl. stripped exports and
  `__aiGatewayManaged`), scan for hardcoded keys, and `Prepare` (rewrite trigger path, auth off,
  patch credential ids, drop pinData).
- `internal/runner`: Docker CLI pool of fresh n8n containers from the baked image
  `meow-judge-n8n:2.47.4` (owner + API key baked in; key in an image label). `Execute` creates
  credentials and the workflow over n8n's public API, publishes, waits for the webhook, runs tests
  (4 in parallel), reads failed executions to attach errors (matched by a random per-step nonce header kept on the worker, never the test id, or by chat
  session), detects 429s (retries once) and rejected keys.
- `internal/problem`: `problems/<dir>/problem.yaml`, `statement.md`, `tests/*.yaml`. Tests have
  seeded random `vars`, computed `let`, steps, and `expect` checks. Rendering is deterministic per
  seed, so expected values are never stored or sent to workers.
- `internal/mock` + `judge mock`: fake Telegram Bot API (:80) and Google Calendar (:443, TLS with a CA
  made at bake time) in the `meow-judge-mock` container at 172.30.99.2 on `meow-judge-net`. Judge
  containers send `www.googleapis.com`/`oauth2…` there via `--add-host` and trust the CA
  (`NODE_EXTRA_CA_CERTS`); Telegram credentials get `baseUrl` = the mock. Each test is a mock
  "tenant" keyed by its bot token / OAuth access token. Problems list simulated credential types in
  `mocked:`; teams enter nothing for them.
- Telegram problems (`trigger: telegram`, `internal/runner/telegram.go`): one workflow copy per test
  (own tenant, own webhookId), the judge posts fake updates with the secret from `setWebhook`, waits
  for the execution, and the step body is `{reply, replies, reply_count, calendar{events,created,calls}}`.
- `internal/grade`: verdicts (Accepted, Wrong Answer, Runtime Error, Time Limit, Key Rate-Limited,
  Key Rejected, Model Unavailable, Invalid Workflow, Judge Error) and the rubric grader (`Chain` of explicit OpenAI-compatible providers with failover and a cache).
- `internal/server`: team pages and JSON API, admin pages (rounds, evaluator table, CSV, run
  detail), FIFO queue, and the worker long-poll API (`/api/worker/claim`, `/api/worker/result`).
  Team keys live only in memory (`keyStore`) until a worker claims the run.

## Event build (Automathon 2.0)

- Two rounds: `problems/1-mess`, `problems/2-emergency`. Multi-entry problems list `entries:` (fixed
  webhook paths) in `problem.yaml`; `workflow.MapEntries` finds them, `PrepareEntries` rewrites the
  paths, switches other triggers off and sets the timezone. `runner/isolated.go` gives every test
  its own workflow copy and simulated Google/Telegram accounts; step body = the webhook response,
  `expect.mock` checks what the simulators saw during that step (telegram/email deltas).
- `expect.alerts` compares sent alerts with `alert_rules` + `directory` in `problem.yaml`.
- Runs are `sample` (visible tests, 2 min throttle) or `attempt` (3 per round, best counts, judge-side
  failures refunded and logged as incidents). Marks, offline scores and board visibility are admin
  settings; `/board` is the public leaderboard and `/admin/export` the CSV.
- The mock admin API needs a per-start bearer token (`MOCK_ADMIN_TOKEN`); the runner passes it.
- Use `problem.NewJob` and `RenderAt(seed, runID, refTime)` so dates are frozen per run.
- Rubric checks: `rubric_fast` regexes pass without the LLM; a miss goes to the grader (a pattern can only pass a
  team, never fail one). Steps can reuse earlier replies as `<<stepN.path>>`; `sheets:` seeds the fake spreadsheets.
- Grader outage or a test still rate-limited after retries = Judge Error: attempt refunded, incident logged, never a zero.
  A model the provider rejects (retired, no tool/JSON support) = `Model Unavailable`, which counts (samples are free).
- Teams may use any AI provider; statements are provider-neutral and contain only MUST-USE contract text (paths,
  request/reply JSON, graded rules). Behind-the-scenes detail stays out of statements and PDFs.
- A required webhook missing from an upload = Invalid Workflow (decided; no lenient mode). `/api/starter` serves a
  paste-able starter generated from `entries:`.
- Admin run data is kept broad (full outputs, per-step simulator snapshots, grader provider and reasoning).

## Conventions

- Hidden tests never reach teams at all: no id, category, verdict row or timing. `viewRun(run, false)` drops them and adds `hidden{passed,total}` plus run-level `notices` (rate-limited, key rejected, model unavailable...); `/api/state` lists only visible tests. Admin sees everything.
- Team POST APIs require `Content-Type: application/json` (CSRF guard with SameSite=Lax cookies).
- Never log request bodies or credentials. Keys never go to SQLite.
- Problem tests: timing limits (`max_ms`) ≥ 8000, since n8n's Code-node runner takes ~2s to warm up.

## Gotchas

- n8n 2.x: "activate" is "publish" (`POST /api/v1/workflows/{id}/publish`). Webhooks register about
  a second after publishing; probe with GET (a 404 saying "Did you mean…" means registered).
- OPTIONS returns non-404 for any path (CORS), so it can't detect webhook registration.
- `/types/nodes.json` lists a node type several times with different version sets; merge them.
- A `--rm` container is deleted by `docker stop`, so bake runs without `--rm` and commits.
- One judge process with containers per machine: `serve --local-pool`, `worker` and `check` all
  remove every `meow.judge` container on start and share the `meow-judge-mock` container name.
- A crashed n8n execution is Runtime Error even if its checks "pass" (e.g. "nothing was booked").
- YAML flow maps: quote paths with brackets (`path: "calendar.created[0].date"`), or `[0]` parses as a list.
- The scratch mock image has no zoneinfo: `internal/mock` imports `time/tzdata`.
- n8n's Telegram node appends "This message was sent automatically with n8n"; the mock strips it.
- n8n validates node parameters on publish (e.g. a Calendar resource-locator in the wrong mode).
- Gemini free tier: 2.5 models are retired (404); `gemini-3.1-flash-lite` is the grader/ideal model (15 RPM, 500 RPD);
  3.8-flash and flash-latest hang or 503. Mistral's free plan is gone; Ollama on a LAN laptop is the planned backup.
- The fake Sheets starts blank with no header: fixed-column Sheets appends fail ("Column names were updated"), so
  statements say tab `Sheet1`/`Incidents` by name plus auto-map or own header row.
- Run `bin/judge check` through `flock /tmp/judge-check.lock` when several agents share the machine; checks that
  queue behind a long run also eat any `timeout` wrapped around them.
- Never run Docker (or install it) in a second WSL distro on the same machine: distros share one network, and the second daemon flushed the
  first one's forwarding rules, so judge containers lost internet. Fix: stop the judge, remove the `meow-judge-net` network and containers,
  let the next start recreate the network. Test worker scripts on a separate machine or VM, not in a sibling WSL distro.
- Playwright MCP can only upload files from inside the repo (`.playwright-mcp/`, gitignored).

## Not built

No public leaderboard, plagiarism/similarity check, gVisor, or HA for the central box. No egress
filtering: a team's HTTP Request node can reach the worker's LAN and host bridge IP (fix: root-run
DOCKER-USER rules dropping RFC1918/169.254.0.0/16 from judge containers). Containers do run with
`--cap-drop ALL` and `no-new-privileges`. Python Code nodes (need the `n8nio/runners` sidecar). OAuth credentials (Gmail, Sheets) are
rejected at pre-check. Mock server for external services: only if a problem needs it. A webhook set to send no data still fails (only n8n's
"Workflow was started" reply is replaced by the last node's output).
