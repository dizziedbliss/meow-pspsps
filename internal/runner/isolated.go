package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/mock"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

// executeIsolated runs a multi-entry webhook problem. Every test gets its own copy of the workflow
// (own webhook paths) and its own simulated Google/Telegram accounts, so tests can't see each
// other's data and run in parallel. After each step the judge waits for that step's execution to
// finish, then records what the simulated services saw during the step.
func executeIsolated(ctx context.Context, c *container, env *Env, job *Job) *Result {
	start := time.Now()
	res := &Result{RunID: job.RunID, Tests: make([]TestResult, len(job.Tests))}
	defer func() { res.Seconds = time.Since(start).Seconds() }()
	cl := newClient(c.Base, env.APIKey)

	shared := map[string]workflow.CredRef{}
	for _, need := range job.Needs {
		if need.Mocked {
			if env.MockAdmin == "" {
				res.Infra = "this test machine has no service simulator running"
				return res
			}
			continue
		}
		data := map[string]string{}
		for _, f := range need.Fields {
			v := strings.TrimSpace(job.Creds[need.Type][f.Name])
			if v == "" {
				v = f.Default
			}
			if v != "" {
				data[f.Name] = v
			}
		}
		name := "team " + need.DisplayName
		id, err := cl.createCredential(ctx, name, need.Type, data)
		if err != nil {
			res.Invalid = fmt.Sprintf("the %s credential was refused: %s", need.DisplayName, errMsg(err))
			return res
		}
		shared[need.Type] = workflow.CredRef{ID: id, Name: name}
	}

	timeout := time.Duration(job.StepTimout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	var mu sync.Mutex
	var firstInvalid string
	var wg sync.WaitGroup
	sem := make(chan struct{}, job.parallel())
	for i := range job.Tests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var tr TestResult
			var invalid string
			// A 429 from the team's own key isn't their workflow's fault: wait and try again, twice.
			for attempt := 0; attempt < 3; attempt++ {
				tr, invalid = runIsolatedTest(ctx, cl, env, job, &job.Tests[i], shared, timeout)
				if invalid != "" || !tr.RateLimited {
					break
				}
				tr.Retried = true
				select {
				case <-time.After(time.Duration(10*(attempt+1)) * time.Second):
				case <-ctx.Done():
				}
			}
			res.Tests[i] = tr
			if invalid != "" {
				mu.Lock()
				if firstInvalid == "" {
					firstInvalid = invalid
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	res.Invalid = firstInvalid
	return res
}

func runIsolatedTest(ctx context.Context, cl *n8nClient, env *Env, job *Job, t *JobTest, shared map[string]workflow.CredRef, timeout time.Duration) (TestResult, string) {
	tr := TestResult{ID: t.ID}
	tenant := fmt.Sprintf("%d:%s", 7000000000+time.Now().UnixNano()%999999999, randHex(16))
	infra := func(msg string) (TestResult, string) {
		tr.Steps = []StepResult{{ConnErr: msg, Infra: true}}
		return tr, ""
	}
	haveMock := env.MockAdmin != ""
	if haveMock {
		seed := t.Seed
		if seed == nil {
			seed = &mock.Seed{}
		}
		if seed.Timezone == "" {
			seed.Timezone = job.Timezone
		}
		seed = withTeamHeaders(seed, job.SheetHeaders)
		if err := mockPut(ctx, env, tenant, seed); err != nil {
			return infra("simulator: " + err.Error())
		}
		defer mockDelete(env, tenant)
	}

	refs := map[string]workflow.CredRef{}
	for k, v := range shared {
		refs[k] = v
	}
	for _, need := range job.Needs {
		if !need.Mocked {
			continue
		}
		name := "judge " + need.DisplayName + " " + t.ID
		id, err := cl.createCredentialAny(ctx, name, need.Type, mockCredData(need.Type, tenant))
		if err != nil {
			return infra("simulated " + need.DisplayName + " credential: " + errMsg(err))
		}
		refs[need.Type] = workflow.CredRef{ID: id, Name: name}
	}

	prefix := "judge-" + randHex(6)
	body, err := workflow.PrepareEntries(job.Workflow, job.Entries, job.Needs, refs, prefix, job.Timezone)
	if err != nil {
		return tr, err.Error()
	}
	wfID, err := cl.createWorkflow(ctx, body)
	if err != nil {
		return tr, "n8n couldn't import the workflow: " + errMsg(err)
	}
	if err := cl.publish(ctx, wfID); err != nil {
		return tr, "n8n couldn't publish (activate) the workflow: " + errMsg(err)
	}
	post := workflow.Trigger{Kind: workflow.TriggerWebhook, Method: "POST"}
	for _, e := range job.Entries {
		if err := waitRegistered(ctx, cl.base+"/webhook/"+prefix+"-"+e.Name, post); err != nil {
			return infra(err.Error())
		}
	}

	sentTG, sentMail := 0, 0
	for si, st := range t.Steps {
		if st.WaitSec > 0 {
			select {
			case <-time.After(time.Duration(st.WaitSec) * time.Second):
			case <-ctx.Done():
			}
		}
		st.Body = json.RawMessage(ExpandRefs(string(st.Body), tr.Steps, true))
		st.Raw = ExpandRefs(st.Raw, tr.Steps, false)
		u := cl.base + "/webhook/" + prefix + "-" + st.Entry
		before := countFinished(ctx, cl, wfID)
		nonce := randHex(10) // not the test id: the workflow can see this header
		sr := doStep(ctx, u, post, nonce, si, st, timeout)
		if !sr.TimedOut && sr.ConnErr == "" {
			// The workflow may keep going after it answers (alerts, sheet writes): wait for the end.
			if !waitExecutionFinished(ctx, timeout, func() bool { return countFinished(ctx, cl, wfID) > before }) {
				sr.TimedOut = true
			} else {
				time.Sleep(150 * time.Millisecond)
			}
		}
		_ = latestExecError(ctx, cl, wfID, &sr)
		useLastNodeIfAcknowledged(ctx, cl, wfID, &sr) // webhook set to respond immediately: the last node's output is the answer
		if haveMock {
			raw, err := mockGetRaw(ctx, env, tenant)
			if err != nil {
				sr.ConnErr, sr.Infra = "simulator: "+err.Error(), true
			} else {
				sr.Mock, sentTG, sentMail = deltaSnapshot(raw, sentTG, sentMail)
			}
		}
		tr.Steps = append(tr.Steps, sr)
		if sr.ExecError != "" {
			if reRateLimit.MatchString(sr.ExecError) {
				tr.RateLimited = true
			} else if reModelBad.MatchString(sr.ExecError) {
				tr.ModelProblem = true
			} else if reKeyBad.MatchString(sr.ExecError) {
				tr.KeyRejected = true
			}
		}
		if sr.TimedOut || sr.ConnErr != "" {
			break
		}
	}
	return tr, ""
}

// deltaSnapshot keeps only the Telegram messages and emails sent since the previous step; other
// parts (sheets, calendar) are shown as they are now. It returns the new totals.
func deltaSnapshot(raw map[string]any, tg, mail int) (string, int, int) {
	cut := func(section string, from int) int {
		m, _ := raw[section].(map[string]any)
		if m == nil {
			return from
		}
		arr, _ := m["sent"].([]any)
		total := len(arr)
		if from > total {
			from = total
		}
		m["sent"] = arr[from:]
		m["sent_total"] = total
		return total
	}
	tg = cut("telegram", tg)
	mail = cut("email", mail)
	b, _ := json.Marshal(raw)
	return string(b), tg, mail
}

func mockGetRaw(ctx context.Context, env *Env, tenant string) (map[string]any, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", env.MockAdmin+"/admin/tenants/"+url.PathEscape(tenant), nil)
	mockAuth(req, env)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("status %d %s", resp.StatusCode, b)
	}
	var m map[string]any
	return m, json.NewDecoder(resp.Body).Decode(&m)
}

// withTeamHeaders adds, to the "any spreadsheet" template, an empty tab with the team's own column
// headers for every tab the problem's seed doesn't already define.
func withTeamHeaders(seed *mock.Seed, headers map[string][]string) *mock.Seed {
	if len(headers) == 0 {
		return seed
	}
	cp := *seed
	cp.Sheets = map[string]mock.SeedSpreadsheet{}
	for k, v := range seed.Sheets {
		cp.Sheets[k] = v
	}
	tpl := cp.Sheets["*"]
	have := map[string]bool{}
	for _, sh := range tpl.Sheets {
		have[sh.Title] = true
	}
	tabs := append([]mock.SeedSheet{}, tpl.Sheets...)
	for tab, cols := range headers {
		if !have[tab] {
			tabs = append(tabs, mock.SeedSheet{Title: tab, Header: cols})
		}
	}
	tpl.Sheets = tabs
	cp.Sheets["*"] = tpl
	return &cp
}

// waitExecutionFinished prevents an early webhook response from being graded before its workflow finishes.
func waitExecutionFinished(ctx context.Context, timeout time.Duration, finished func() bool) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		if finished() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-tick.C:
		}
	}
}
