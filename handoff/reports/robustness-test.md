# Robustness test: VeryJudgyCat with broken and hostile workflows

Status: IN PROGRESS (updated as cases finish).

**Central site: NOT restarted by this test.** At 05:52 Max started the real central himself with a public tunnel, which wiped all judge containers and
replaced the shared mock container (new admin token). The coordinator then restarted the real central with `--pool 0` (it no longer touches docker)
and asked this test to leave it alone. The real central keeps running with `--pool 0` (no worker) and the tunnel until the load test agent and the
coordinator restore the pool. Results of this test from about 05:51 to 05:52 were discarded and re-run after restarting the throwaway server.

Setup: throwaway server on 127.0.0.1:18555, temp db in the scratchpad (`.../scratchpad/rt/`), 6 fake teams ZZ01..ZZ06, rounds `1=mess,2=2-emergency`,
2 local worker slots, grader from `~/.config/meow-judge/grader.json`. Driver: `scratchpad/rt/rt.py` (JSON API, same calls as the browser).
Baseline: Mess ideal sample run = 2/2 Accepted in 8 s.

## Results so far

### Group 1: uploads (POST /api/analyze)

| Case | Result | Verdict |
|---|---|---|
| no `workflow` field | 200, error "the file is empty" | OK |
| workflow = "" or a string | 200, error "not valid JSON: json: cannot unmarshal string into Go value of type map[string]interface {}" | Works but leaks Go jargon (low) |
| body is not JSON at all | 400 "that file isn't valid JSON (or is over 5 MB)" | OK |
| `[]` | "expected one workflow, found 0" | OK |
| `{}` / no nodes / nodes null | "no nodes found; export the workflow from n8n…" | OK |
| array with two workflows | "expected one workflow, found 2" | OK |
| node is not an object | "node 0 is not an object" | OK |
| 15 MB file | connection broken (client gets Broken pipe), no clean message | Browser would show a network error (see bugs) |
| 4 MB file | accepted | OK |
| hardcoded fake OpenAI + Google keys | warnings only, analysis continues | OK |
| non-JSON content type | 415 "send JSON" | OK |

### Group 2: pre-check

| Case | Result | Verdict |
|---|---|---|
| no trigger | both required webhooks missing, lists team's paths ("none") | OK |
| wrong webhook path | names the missing path and lists the team's paths | OK |
| duplicate path | second entry reported missing | OK |
| required webhook disabled | reported missing | OK |
| webhooks set to GET | "accepts GET, but tests send POST; set its HTTP Method to POST" | OK |
| unknown node type | "isn't available on the judge (n8n 2.47.4)" | OK |
| community node | "a community node; the judge only runs built-in n8n nodes" | OK |
| typeVersion 9.9 | lists the versions the judge has | OK |
| Python Code node | NO error and NO warning | Gap (see bugs) |
| Execute Workflow node (unconnected) | warning only ("not reachable") | Runtime case still to test |
| Execute Command node | rejected as unavailable | OK |
