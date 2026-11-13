# VeryJudgyCat: n8n workflow judge for Automathon 2.0

VeryJudgyCat is a LeetCode-style judge for n8n workflows, built for the Automathon 2.0 hackathon. Teams upload an exported workflow, the judge runs it in a
fresh n8n 2.47.4 container against sample and hidden tests, and shows a verdict per sample test and how many hidden tests passed. Teams get free
sample runs and 3 scored attempts per round (best counts). Organisers see attempts, scores, incidents and a leaderboard, and can download a CSV.

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

One command (after the build and `bin/judge bake`):

```bash
deploy/start-central.sh --make-teams --pool 2        # first time: also creates the 86 team logins in teams.csv
deploy/start-central.sh --pool 2                     # later restarts
```

It generates the admin password and worker token once (`~/.config/meow-judge/admin.pw`, `worker.token`), starts the site with a fresh
`event.db`, rounds `1=mess,2=2-emergency` and the grader list in `~/.config/meow-judge/grader.json`, and opens a Cloudflare tunnel
(quick tunnel by default, `--tunnel NAME` for a named one). It prints the address teams open, the admin login and the exact command to
add a worker PC. Add `--no-tunnel` to restart only the site and keep a running tunnel (the script refuses to start if a site already answers). Stop it with `kill $(cat logs/central.pid logs/tunnel.pid)`. Print `teams.csv`, keep it private, never commit it.

The grader provider is always explicit (`JUDGE_LLM_CONFIG=grader.json`: a list of providers tried in order, each with `base`, `model`,
`key_file`, `gap_ms`; or `JUDGE_LLM_BASE` + `JUDGE_LLM_MODEL` + `JUDGE_LLM_KEY[_FILE]` for one). Nothing is guessed from a key prefix. For many Gemini keys use `"keys_file": "~/.config/meow-judge/gemini-keys.txt"` (one key per line, `#` comments allowed): each key becomes its own provider, used one after the other (a key that gets rate-limited rests for a minute while the next takes over). Use keys from different Google projects, since each project has its own free limit. A grader
outage is a judge-side failure: the attempt is refunded, never scored zero.

### 3. Other workers

Linux PCs without Docker: `sudo ./veryjudgycat.sh install` from the worker bundle (see the section at the end). A machine that already has
Docker and the baked images can also run `bin/judge worker --central https://<site> --token <worker token> --pool 2`.
They show up under "Test machines" on `/admin`. Workers only make outbound HTTPS calls.

### 4. During the event

- `/admin`: open round 1. "Open round" closes any other open round; "Open too" keeps both open, and teams then switch between round tabs and submit to either (attempts are counted per round, one run at a time per team). Set marks per test and the maximum per round, enter offline
  scores per team, and choose which rounds `/board` shows. Open incidents (things that broke on our side) are listed at the top.
- Teams log in at `/` with the code and password from `teams.csv`: 3 scored attempts per round (best counts), unlimited free samples.
- The private practice account `PPOOL` (unlimited runs, previews the pending round, not on `/board` or the CSV) is for checking the site before teams log in. The participant guide is at `/static/participant-guide.pdf`; `HANDBOOK.md` has the full event-day steps.
- "Download CSV" gives the leaderboard with judge and offline scores per round. Each run's workflow JSON is on its detail page.

### If something breaks

- Central restarts: runs that were queued or running are marked failed ("please Run again"), because
  keys live only in memory. Submissions and scores are safe in `event.db`.
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

### Worker PCs (college lab, Linux, no Docker yet)

On the organiser machine, after the final build and bake: `./deploy/make-bundle.sh` writes `dist/worker-bundle/` (about 350 MB:
the Linux binary, the saved judge images, install and uninstall scripts). Copy that folder to each PC (USB, shared drive or
`python3 -m http.server`), boot Linux, and run:

    sudo ./veryjudgycat.sh install --central https://<site> --pool 2     # --dry-run shows the steps first

It installs Docker from the distro repos if missing (Debian/Ubuntu; elsewhere install Docker and add `--skip-docker`), loads the images,
and starts a `veryjudgycat-worker` service. The token sits in a root-only file. Pool 2 suits a 6 GB PC. After the event:

    sudo ./veryjudgycat.sh uninstall      # stops the service, removes containers, images, /opt/veryjudgycat, and Docker if we installed it (`--keep-docker` to keep it)

Then delete the copied folder. Test the worker installer and connectivity on one college PC before installing on the others.

## Event content is not in this repo

The real problem packs (`problems/1-mess`, `problems/2-emergency`), their hidden tests and the organiser
test-case PDFs live only on the organiser's machine and are gitignored. `problems/0-dummy` is a complete
example of the format. To run your own event, add packs under `problems/<dir>/` and pass them with
`--rounds 1=<dir>,2=<dir>`. Tests that need a real event pack skip themselves when it is absent.
