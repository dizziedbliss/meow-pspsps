package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/mock"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

// Job is one Run: a team's workflow, their keys, and the test inputs. Expected outputs never leave
// the central server; a worker only executes and reports raw results.
type Job struct {
	RunID      int64                        `json:"runId"`
	Workflow   json.RawMessage              `json:"workflow"`
	Trigger    workflow.Trigger             `json:"trigger"`
	Needs      []workflow.CredNeed          `json:"needs"`
	Creds      map[string]map[string]string `json:"creds"` // credential type → field → value
	Tests      []JobTest                    `json:"tests"`
	StepTimout int                          `json:"stepTimeoutSec"`
	Parallel   int                          `json:"parallel,omitempty"`
}

func (j *Job) parallel() int {
	if j.Parallel > 0 {
		return j.Parallel
	}
	return 4
}

// JobTest is one test case's requests, in order. Steps in a test share state (e.g. chat sessions).
type JobTest struct {
	ID    string    `json:"id"`
	Steps []JobStep `json:"steps"`
	Seed  *MockSeed `json:"seed,omitempty"` // fake calendar contents for this test
}

// MockSeed and MockSeedItem set up a test's simulated services.
type (
	MockSeed     = mock.Seed
	MockSeedItem = mock.SeedItem
)

// JobStep is one request to the workflow.
type JobStep struct {
	Method  string            `json:"method,omitempty"` // webhook; default POST
	Query   map[string]string `json:"query,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`    // webhook JSON body
	Raw     string            `json:"raw,omitempty"`     // webhook raw body (sent as-is, for malformed-input tests)
	Session string            `json:"session,omitempty"` // chat session id
	Message string            `json:"message,omitempty"` // chat / telegram text
	From    string            `json:"from,omitempty"`    // telegram sender's first name
}

// Result is what a worker reports for a Job.
type Result struct {
	RunID   int64        `json:"runId"`
	Invalid string       `json:"invalid,omitempty"` // n8n refused the workflow or a credential
	Infra   string       `json:"infra,omitempty"`   // judge-side failure; not the team's fault
	Tests   []TestResult `json:"tests"`
	Worker  string       `json:"worker"`
	Seconds float64      `json:"seconds"`
}

// TestResult is the raw outcome of one test.
type TestResult struct {
	ID          string       `json:"id"`
	Steps       []StepResult `json:"steps"`
	RateLimited bool         `json:"rateLimited,omitempty"`
	KeyRejected bool         `json:"keyRejected,omitempty"`
	Retried     bool         `json:"retried,omitempty"`
}

// StepResult is one request's raw outcome.
type StepResult struct {
	Status     int    `json:"status"`
	Body       string `json:"body"`
	DurationMs int64  `json:"durationMs"`
	TimedOut   bool   `json:"timedOut,omitempty"`
	ConnErr    string `json:"connErr,omitempty"`
	ExecError  string `json:"execError,omitempty"` // n8n's error message for this request, if it failed
	ErrorNode  string `json:"errorNode,omitempty"`
	Failed     bool   `json:"failed,omitempty"` // the n8n execution itself ended in error
}

var (
	reRateLimit = regexp.MustCompile(`(?i)\b429\b|rate.?limit|quota|too many requests|resource.?exhausted|overloaded`)
	reKeyBad    = regexp.MustCompile(`(?i)authorization failed|incorrect api key|invalid api key|api key not valid|invalid x-api-key|\b401\b|unauthorized|permission denied`)
)

const maxBody = 256 << 10 // keep at most 256 KB of each response

// noRedirect calls team workflows: a redirect answer is recorded, never followed (it could point
// the worker at its own network).
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Env is what a worker knows about its judge setup.
type Env struct {
	APIKey    string
	MockAdmin string // http://127.0.0.1:<port> of this worker's mock (empty if none)
}

// Execute runs a job in an already-started container. The caller destroys the container after.
func Execute(ctx context.Context, c *container, env *Env, job *Job) *Result {
	if job.Trigger.Kind == workflow.TriggerTelegram {
		return executeTelegram(ctx, c, env, job)
	}
	apiKey := env.APIKey
	start := time.Now()
	res := &Result{RunID: job.RunID, Tests: []TestResult{}}
	defer func() { res.Seconds = time.Since(start).Seconds() }()
	cl := newClient(c.Base, apiKey)

	refs := map[string]workflow.CredRef{}
	for _, need := range job.Needs {
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
		refs[need.Type] = workflow.CredRef{ID: id, Name: name}
	}

	path := "judge-" + randHex(8)
	body, err := workflow.Prepare(job.Workflow, job.Trigger, job.Needs, refs, path)
	if err != nil {
		res.Invalid = err.Error()
		return res
	}
	wfID, err := cl.createWorkflow(ctx, body)
	if err != nil {
		res.Invalid = "n8n couldn't import the workflow: " + errMsg(err)
		return res
	}
	if err := cl.publish(ctx, wfID); err != nil {
		res.Invalid = "n8n couldn't publish (activate) the workflow: " + errMsg(err)
		return res
	}

	url := c.Base + "/webhook/" + path
	if job.Trigger.Kind == workflow.TriggerChat {
		url += "/chat"
	}
	if err := waitRegistered(ctx, url, job.Trigger); err != nil {
		res.Infra = err.Error()
		return res
	}

	timeout := time.Duration(job.StepTimout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	res.Tests = make([]TestResult, len(job.Tests))
	runTest := func(t JobTest) TestResult {
		tr := TestResult{ID: t.ID}
		for si, st := range t.Steps {
			tr.Steps = append(tr.Steps, doStep(ctx, url, job.Trigger, t.ID, si, st, timeout))
		}
		return tr
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, job.parallel()) // tests run in parallel inside one container
	for i, t := range job.Tests {
		wg.Add(1)
		go func(i int, t JobTest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res.Tests[i] = runTest(t)
		}(i, t)
	}
	wg.Wait()

	attachExecErrors(ctx, cl, wfID, job, res)

	// A 429 from the team's own key isn't their workflow's fault: wait, then try those tests once more.
	var again []int
	for i := range res.Tests {
		if res.Tests[i].RateLimited {
			again = append(again, i)
		}
	}
	if len(again) > 0 {
		select {
		case <-time.After(8 * time.Second):
		case <-ctx.Done():
		}
		for _, i := range again {
			res.Tests[i] = runTest(job.Tests[i])
			res.Tests[i].Retried = true
		}
		attachExecErrors(ctx, cl, wfID, job, res)
	}
	return res
}

func errMsg(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// waitRegistered waits for n8n to register the webhook after publishing (about a second). It probes
// with GET, which never runs a POST-only workflow: n8n's 404 says "Did you mean to make a POST
// request?" once the path exists, and plain "is not registered" before.
func waitRegistered(ctx context.Context, url string, trig workflow.Trigger) error {
	if trig.Kind == workflow.TriggerWebhook && strings.Contains(trig.Method, "GET") {
		time.Sleep(2 * time.Second) // a GET probe would run the workflow; doStep retries instead
		return nil
	}
	hc := &http.Client{Timeout: 5 * time.Second}
	for i := 0; i < 60; i++ {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := hc.Do(req)
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if !notRegistered(resp.StatusCode, b) {
				return nil
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("the webhook never registered after publishing")
}

// notRegistered is n8n's answer for a path with no webhook at all (not a wrong-method answer).
func notRegistered(status int, body []byte) bool {
	return status == 404 && bytes.Contains(body, []byte("is not registered")) && !bytes.Contains(body, []byte("Did you mean"))
}

func doStep(ctx context.Context, url string, trig workflow.Trigger, testID string, idx int, st JobStep, timeout time.Duration) StepResult {
	method := "POST"
	var body []byte
	headers := map[string]string{"Content-Type": "application/json"}
	if trig.Kind == workflow.TriggerChat {
		body, _ = json.Marshal(map[string]string{"action": "sendMessage", "sessionId": st.Session, "chatInput": st.Message})
	} else {
		if st.Method != "" {
			method = st.Method
		}
		switch {
		case st.Raw != "":
			body = []byte(st.Raw)
		case len(st.Body) > 0:
			body = st.Body
		}
		if len(st.Query) > 0 {
			q := make([]string, 0, len(st.Query))
			for k, v := range st.Query {
				q = append(q, k+"="+v)
			}
			url += "?" + strings.Join(q, "&")
		}
	}
	for k, v := range st.Headers {
		headers[k] = v
	}
	headers["X-Judge-Test"] = fmt.Sprintf("%s/%d", testID, idx)

	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, _ := http.NewRequestWithContext(sctx, method, url, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	t0 := time.Now()
	resp, err := noRedirect.Do(req)
	// Just after publishing, n8n can still answer "not registered" for a moment: retry briefly.
	for tries := 0; err == nil && tries < 40 && resp.StatusCode == 404; tries++ {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if !notRegistered(404, b) {
			resp.Body = io.NopCloser(bytes.NewReader(b))
			break
		}
		time.Sleep(250 * time.Millisecond)
		if body != nil {
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		t0 = time.Now()
		resp, err = noRedirect.Do(req)
	}
	sr := StepResult{DurationMs: time.Since(t0).Milliseconds()}
	if err != nil {
		if sctx.Err() == context.DeadlineExceeded {
			sr.TimedOut = true
		} else {
			sr.ConnErr = err.Error()
		}
		return sr
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	sr.DurationMs = time.Since(t0).Milliseconds()
	sr.Status = resp.StatusCode
	sr.Body = string(data)
	return sr
}

// attachExecErrors finds failed executions and pins their error message on the step that caused
// them, matched by the X-Judge-Test header (webhook) or session id (chat).
func attachExecErrors(ctx context.Context, cl *n8nClient, wfID string, job *Job, res *Result) {
	execs, err := cl.executions(ctx, wfID, 250)
	if err != nil {
		return
	}
	sessionStep := map[string][2]int{} // chat session → (test, step) of the latest step using it
	for ti, t := range job.Tests {
		for si, st := range t.Steps {
			if st.Session != "" {
				sessionStep[st.Session] = [2]int{ti, si}
			}
		}
	}
	testIdx := map[string]int{}
	for i, t := range job.Tests {
		testIdx[t.ID] = i
	}
	for _, e := range execs {
		rd := e.Data.ResultData
		msg, node := "", ""
		if rd.Error != nil {
			msg = strings.TrimSpace(rd.Error.Message + " " + rd.Error.Description)
			if rd.Error.Node != nil {
				node = rd.Error.Node.Name
			}
		}
		if msg == "" {
			// Errors in sub-nodes (e.g. the chat model under an agent) may only show on the node.
			for name, runs := range rd.RunData {
				for _, r := range runs {
					if r.Error != nil {
						msg = strings.TrimSpace(r.Error.Message + " " + r.Error.Description)
						node = name
					}
				}
			}
		}
		if msg == "" {
			continue
		}
		ti, si, ok := locate(rd.RunData, job.Trigger.NodeName, testIdx, sessionStep)
		if !ok {
			continue
		}
		if si >= len(res.Tests[ti].Steps) {
			continue
		}
		sr := &res.Tests[ti].Steps[si]
		sr.ExecError, sr.ErrorNode = msg, node
		sr.Failed = e.Status == "error" || e.Status == "crashed"
		if reRateLimit.MatchString(msg) {
			res.Tests[ti].RateLimited = true
		} else if reKeyBad.MatchString(msg) {
			res.Tests[ti].KeyRejected = true
		}
	}
}

func locate(runData map[string][]nodeRunRecord, trigger string, testIdx map[string]int, sessionStep map[string][2]int) (int, int, bool) {
	runs := runData[trigger]
	if len(runs) == 0 || len(runs[0].Data.Main) == 0 || len(runs[0].Data.Main[0]) == 0 {
		return 0, 0, false
	}
	j := runs[0].Data.Main[0][0].JSON
	if h, ok := j["headers"].(map[string]any); ok {
		if v, ok := h["x-judge-test"].(string); ok {
			id, step, found := strings.Cut(v, "/")
			if ti, ok := testIdx[id]; found && ok {
				var si int
				fmt.Sscanf(step, "%d", &si)
				return ti, si, true
			}
		}
	}
	if s, ok := j["sessionId"].(string); ok {
		if p, ok := sessionStep[s]; ok {
			return p[0], p[1], true
		}
	}
	return 0, 0, false
}
