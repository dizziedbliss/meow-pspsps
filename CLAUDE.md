# meow-pspsps: n8n workflow judge

LeetCode-style judge for a hackathon: teams upload an exported n8n workflow, the judge runs it in a
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
  (optional `JUDGE_LLM_KEY`, `JUDGE_LLM_BASE`, `JUDGE_LLM_MODEL` for rubric grading)
- Worker on another machine: `bin/judge worker --central https://<site> --token <worker token> --pool N`
- Team logins: `bin/judge teams --db judge.db --count 100 --out teams.csv`

## Architecture

- One Go binary (`cmd/judge`), SQLite (`modernc.org/sqlite`, pure Go), server-rendered HTML plus
  `web/static/app.js`, all embedded with `go:embed`.
- `internal/workflow`: parse an export, find the single Webhook/Chat Trigger, pre-check node types
  and versions against `internal/catalog`, detect needed credentials (incl. stripped exports and
  `__aiGatewayManaged`), scan for hardcoded keys, and `Prepare` (rewrite trigger path, auth off,
  patch credential ids, drop pinData).
- `internal/runner`: Docker CLI pool of fresh n8n containers from the baked image
  `meow-judge-n8n:2.47.4` (owner + API key baked in; key in an image label). `Execute` creates
  credentials and the workflow over n8n's public API, publishes, waits for the webhook, runs tests
  (4 in parallel), reads failed executions to attach errors (matched by `X-Judge-Test` header or chat
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
  Key Rejected, Invalid Workflow) and an OpenAI-compatible rubric judge.
- `internal/server`: team pages and JSON API, admin pages (rounds, evaluator table, CSV, run
  detail), FIFO queue, and the worker long-poll API (`/api/worker/claim`, `/api/worker/result`).
  Team keys live only in memory (`keyStore`) until a worker claims the run.

## Conventions

- Hidden tests reach teams as category + verdict only; `viewRun(run, false)` strips the rest.
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
- Playwright MCP can only upload files from inside the repo (`.playwright-mcp/`, gitignored).

## Not built

No public leaderboard, plagiarism/similarity check, gVisor, or HA for the central box. No egress
filtering: a team's HTTP Request node can reach the worker's LAN and host bridge IP (fix: root-run
DOCKER-USER rules dropping RFC1918/169.254.0.0/16 from judge containers). Containers do run with
`--cap-drop ALL` and `no-new-privileges`. Python Code nodes (need the `n8nio/runners` sidecar). OAuth credentials (Gmail, Sheets) are
rejected at pre-check. Mock server for external services: only if a problem needs it.
