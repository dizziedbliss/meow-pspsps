# Load test report (VeryJudgyCat)

Status: **in progress** (last update 05:53 IST, 2026-11-12). This file is updated as the work goes on.

## Task
Load test the judge with 24 fake teams (LT01 to LT24) against a throwaway server, waves A to D, then fix what it exposes. The real central site (127.0.0.1:8080, `event.db`) must be stopped during the test and restarted afterwards.

## Real central site
- State change at 05:52: Max started the real central himself on 127.0.0.1:8080 with a public Cloudflare tunnel and `--pool 0` (no docker). **I do not touch the tunnel (never stop or restart cloudflared).**
- Wait condition changed: the load test starts only after `handoff/reports/robustness-test.md` has a line starting with `FINAL` (or no `judge serve --db .../rt/rt.db` process for 3 minutes in a row; max 60 minutes).
- At the end I restore the real central WITH its pool: `kill $(cat logs/central.pid)` then `./deploy/start-central.sh --pool 2 --no-tunnel` (`--no-tunnel` because the tunnel keeps running with its address).
- Restarted at the end: **not yet** (the load test has not started).

## Setup (ready, not yet run)
- Scratchpad: `/tmp/claude-1000/-home-dizziedbliss-github-meow-pspsps/3d78129d-5b69-4199-a977-390cef3596e9/scratchpad/load/`
  - `stub_gemini.py`: a local stand-in for Gemini's `generateContent` API (about 1.5 s latency, keyword-based answers for the Mess complaint grouping and the Emergency classification). Used so that one shared free Gemini key (15 requests/min) does not distort the infrastructure measurements. Judge containers reach it at `http://172.30.99.1:18501` (the `meow-judge-net` gateway), passed as the `host` field of the team's `googlePalmApi` credential.
  - `loadtest.py`: the driver (logs in 24 teams, opens rounds through the admin form, submits runs at the same moment through `POST /api/run`, polls `GET /api/runs/{id}` for queue positions, samples `docker stats` and `free -m` every 5 s, reads queue wait and run time from the throwaway SQLite DB).
  - `config.json`, `names.txt`, `teams.csv` (fake team logins, throwaway), `lt.db` (throwaway database).
- Throwaway server command (planned): `judge-load serve --db lt.db --rounds 1=mess,2=2-emergency --local-pool 4 --addr 127.0.0.1:18500` with `JUDGE_LLM_CONFIG=$HOME/.config/meow-judge/grader.json`.
- Wave plan: A = 20 free samples at once (Mess ideal); B = 12 scored Mess attempts at once; C = 6 scored Emergency attempts at once (round 2); D = mixed (3 Emergency attempts in round 2 first, then round 1 opens and 10 Mess samples + 6 Mess attempts are submitted; runs in flight when a round closes still finish and count). The round rule means a Mess and an Emergency run cannot be admitted in the same instant, so D is staged this way.

## Results
_Not yet run._

## Peak memory
_Not yet measured._

## Recommended pool size for a 6 GB lab PC
_Pending the measurement._ Earlier idle measurement on the organiser machine: about 390 to 400 MB per judge container, about 23 MB for the fake-services container.

## Bugs fixed (files changed)
_None yet._

## Bugs found, not fixed
_None yet._

## What could not be tested
_Pending._
