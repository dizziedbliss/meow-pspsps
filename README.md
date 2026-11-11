# meow-pspsps: n8n workflow judge

A LeetCode-style judge for n8n workflows. Teams upload an exported workflow, the judge runs it in a
fresh n8n 2.47.4 container against sample and hidden tests, and shows a verdict per test. Teams can
Run as often as they like; Submit is final per round. Organisers see attempts, scores and
submissions, and can download a CSV.

## Event-day runbook

You need: Docker, this repo built as a static binary (`CGO_ENABLED=0 go build -o bin/judge ./cmd/judge`), and `cloudflared` on the
central machine.

### 1. Every machine that runs tests (central box, R740, laptops)

```bash
bin/judge bake            # pulls n8n 2.47.4, builds meow-judge-n8n + meow-judge-mock (~2 min, once)
```

Each n8n container needs about 1 GB of RAM while running. Size the pool to what's free:
an 8 GB laptop handles `--pool 2`, a 64 GB server `--pool 30` or more.

Linux binary for another machine: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o judge-linux ./cmd/judge`
(on Windows laptops, run it inside WSL with Docker Desktop).

### 2. Central machine

```bash
bin/judge teams --db judge.db --count 100 --out teams.csv   # once; print teams.csv, keep it private
export JUDGE_ADMIN_PASSWORD='<long random>'
export JUDGE_WORKER_TOKEN='<another long random>'
export JUDGE_LLM_KEY='<organiser key, used only for rubric grading>'   # optional; or JUDGE_LLM_KEY_FILE=path
# A Google "AIza…" key automatically uses Gemini's OpenAI-compatible endpoint (gemini-2.5-flash).
# export JUDGE_LLM_BASE=https://api.openai.com/v1  JUDGE_LLM_MODEL=gpt-4o-mini
bin/judge serve --db judge.db --rounds 1=<p1>,2=<p2>,3=<p3> --local-pool 4
cloudflared tunnel run --url http://127.0.0.1:8080 <tunnel-name>      # https://judge.<domain>
```

Rubric tests require a working judge; missing or unavailable grading fails the run as an infrastructure error rather than awarding points. Set `JUDGE_LLM_BASE` explicitly for key formats that are not recognized as Gemini keys.

### 3. Other workers

```bash
bin/judge worker --central https://judge.<domain> --token "$JUDGE_WORKER_TOKEN" --pool 20 --name r740
```

They show up under "Test machines" on `/admin`. Workers only make outbound HTTPS calls.

### 4. During the event

- `/admin`: open round 1. Opening another round closes the open one. Closing a round
  auto-submits each team's latest finished run if they never pressed Submit.
- Teams log in at `/` with the code and password from `teams.csv`.
- "Download CSV" per round gives attempts, best and latest score, first 100% time, submission and
  whether it was automatic. Each run's workflow JSON can be downloaded from its detail page.

### If something breaks

- Central restarts: runs that were queued or running are marked failed ("please Run again"), because
  keys live only in memory. Submissions and scores are safe in `judge.db`.
- A worker dies: its runs fail after 12 minutes. Restart it; it cleans up its old containers.
- Check a workflow by hand: `bin/judge check --problem problems/<p> --workflow file.json -v`.

## Writing a problem

`problems/<dir>/problem.yaml`, `statement.md` (shown to teams) and `tests/*.yaml`. See
`problems/0-dummy` for every feature: sample vs hidden tests, random `vars`, computed `let`, raw
malformed bodies, `=json` templated arrays, status/JSON-path/contains/latency checks, and `rubric`
for the LLM judge. Always prove the reference workflow passes:

```bash
bin/judge check --problem problems/<dir> --workflow problems/<dir>/ideal.json --times 3
```
