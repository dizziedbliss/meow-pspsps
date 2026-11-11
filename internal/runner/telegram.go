package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/mock"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

// mockTelegramBase is the Base URL judge containers use for the Telegram API.
const mockTelegramBase = "http://" + mockIP

// executeTelegram runs a Telegram-triggered workflow: every test gets its own copy of the workflow,
// its own fake bot token and fake Google account (a mock "tenant"), so tests run in parallel without
// sharing calendar state or chat memory. The judge plays Telegram: it posts updates to the trigger's
// webhook and collects what the workflow sent back with sendMessage.
func executeTelegram(ctx context.Context, c *container, env *Env, job *Job) *Result {
	start := time.Now()
	res := &Result{RunID: job.RunID, Tests: make([]TestResult, len(job.Tests))}
	defer func() { res.Seconds = time.Since(start).Seconds() }()
	if env.MockAdmin == "" {
		res.Infra = "this test machine has no service simulator running"
		return res
	}
	cl := newClient(c.Base, env.APIKey)

	// Team credentials (their own LLM key) are shared by every test copy.
	shared := map[string]workflow.CredRef{}
	for _, need := range job.Needs {
		if need.Mocked {
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
			tr, invalid := runTelegramTest(ctx, cl, env, job, &job.Tests[i], shared, timeout)
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
	if firstInvalid != "" {
		res.Invalid = firstInvalid
	}
	return res
}

func runTelegramTest(ctx context.Context, cl *n8nClient, env *Env, job *Job, t *JobTest, shared map[string]workflow.CredRef, timeout time.Duration) (TestResult, string) {
	tr := TestResult{ID: t.ID}
	tenant := fmt.Sprintf("%d:%s", 7000000000+time.Now().UnixNano()%999999999, randHex(16))
	seed := t.Seed
	if seed == nil {
		seed = &mock.Seed{Timezone: "UTC"}
	}
	if err := mockPut(ctx, env.MockAdmin, tenant, seed); err != nil {
		tr.Steps = []StepResult{{ConnErr: "simulator: " + err.Error()}}
		return tr, ""
	}
	defer mockDelete(env.MockAdmin, tenant)

	refs := map[string]workflow.CredRef{}
	for k, v := range shared {
		refs[k] = v
	}
	for _, need := range job.Needs {
		if !need.Mocked {
			continue
		}
		name := "judge " + need.DisplayName + " " + t.ID
		data := mockCredData(need.Type, tenant)
		id, err := cl.createCredentialAny(ctx, name, need.Type, data)
		if err != nil {
			tr.Steps = []StepResult{{ConnErr: "simulated " + need.DisplayName + " credential: " + errMsg(err)}}
			return tr, ""
		}
		refs[need.Type] = workflow.CredRef{ID: id, Name: name}
	}

	hook := "judge-" + randHex(8)
	body, err := workflow.Prepare(job.Workflow, job.Trigger, job.Needs, refs, hook)
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
	// Publishing makes the Telegram Trigger call setWebhook on the simulator.
	var snap *mock.Snapshot
	for i := 0; i < 40; i++ {
		snap, err = mockGet(ctx, env.MockAdmin, tenant)
		if err == nil && snap.Telegram.WebhookURL != "" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if snap == nil || snap.Telegram.WebhookURL == "" {
		tr.Steps = []StepResult{{ConnErr: "the Telegram Trigger never registered its webhook with the (simulated) Telegram API"}}
		return tr, ""
	}
	hookURL, err := containerURL(cl.base, snap.Telegram.WebhookURL)
	if err != nil {
		tr.Steps = []StepResult{{ConnErr: "bad webhook URL from the trigger"}}
		return tr, ""
	}
	secret := snap.Telegram.Secret

	sent := 0
	for si, st := range t.Steps {
		sr, newSent := telegramStep(ctx, cl, env, tenant, wfID, hookURL, secret, si, st, sent, timeout)
		sent = newSent
		tr.Steps = append(tr.Steps, sr)
		if sr.ExecError != "" {
			if reRateLimit.MatchString(sr.ExecError) {
				tr.RateLimited = true
			} else if reKeyBad.MatchString(sr.ExecError) {
				tr.KeyRejected = true
			}
		}
	}
	return tr, ""
}

// containerURL takes only the path of the URL the trigger registered and puts it on the container's
// own base, so a workflow can't make the judge call some other host.
func containerURL(base, registered string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(registered)
	if err != nil || !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return "", fmt.Errorf("bad webhook URL")
	}
	out := url.URL{Scheme: b.Scheme, Host: b.Host, Path: path.Clean(u.Path)}
	return out.String(), nil
}

// mockCredData is the credential the judge creates for a simulated service.
func mockCredData(typ, tenant string) map[string]any {
	if typ == "telegramApi" {
		return map[string]any{"accessToken": tenant, "baseUrl": mockTelegramBase}
	}
	// Google OAuth2 credentials: a valid-looking token whose value names the tenant.
	return map[string]any{
		"clientId": "judge-simulated", "clientSecret": "judge-simulated",
		"oauthTokenData": map[string]any{"access_token": tenant, "refresh_token": tenant, "token_type": "Bearer",
			"expires_in": 3599, "scope": "https://www.googleapis.com/auth/calendar"},
	}
}

var updateSeq int64 = 500000

func telegramStep(ctx context.Context, cl *n8nClient, env *Env, tenant, wfID, hookURL, secret string, idx int, st JobStep, sentBefore int, timeout time.Duration) (StepResult, int) {
	from := st.From
	if from == "" {
		from = "Alex"
	}
	chatID, _ := strconv.ParseInt(st.Session, 10, 64)
	updateSeq++
	update := map[string]any{
		"update_id": updateSeq,
		"message": map[string]any{
			"message_id": updateSeq, "date": time.Now().Unix(), "text": st.Message,
			"from": map[string]any{"id": chatID, "is_bot": false, "first_name": from, "language_code": "en"},
			"chat": map[string]any{"id": chatID, "type": "private", "first_name": from},
		},
	}
	buf, _ := json.Marshal(update)
	before := countFinished(ctx, cl, wfID)
	t0 := time.Now()
	req, _ := http.NewRequestWithContext(ctx, "POST", hookURL, bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		return StepResult{ConnErr: err.Error()}, sentBefore
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	sr := StepResult{}
	if resp.StatusCode/100 != 2 {
		sr.Status = resp.StatusCode
		sr.ExecError = fmt.Sprintf("the Telegram Trigger webhook answered HTTP %d", resp.StatusCode)
		return sr, sentBefore
	}

	// Wait for this update's execution to finish (or the time limit).
	deadline := t0.Add(timeout)
	for time.Now().Before(deadline) {
		if countFinished(ctx, cl, wfID) > before {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	sr.DurationMs = time.Since(t0).Milliseconds()
	finished := countFinished(ctx, cl, wfID) > before
	if !finished {
		sr.TimedOut = true
	}
	time.Sleep(150 * time.Millisecond) // let the last sendMessage land
	snap, err := mockGet(ctx, env.MockAdmin, tenant)
	if err != nil {
		sr.ConnErr = "simulator: " + err.Error()
		return sr, sentBefore
	}
	var replies []string
	chat := strconv.FormatInt(chatID, 10)
	for _, m := range snap.Telegram.Sent[min(sentBefore, len(snap.Telegram.Sent)):] {
		if m.ChatID == chat {
			replies = append(replies, m.Text)
		}
	}
	if err := latestExecError(ctx, cl, wfID, &sr); err != nil {
		_ = err
	}
	out := map[string]any{
		"reply":       strings.Join(replies, "\n"),
		"replies":     replies,
		"reply_count": len(replies),
		"calendar":    snap.Calendar,
		"telegram":    map[string]any{"sent_total": len(snap.Telegram.Sent), "calls": snap.Telegram.Calls},
	}
	b, _ := json.Marshal(out)
	sr.Status = 200
	sr.Body = string(b)
	return sr, len(snap.Telegram.Sent)
}

func countFinished(ctx context.Context, cl *n8nClient, wfID string) int {
	var out struct {
		Data []struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	q := url.Values{"workflowId": {wfID}, "limit": {"50"}}
	if err := cl.do(ctx, "GET", "/api/v1/executions?"+q.Encode(), nil, &out); err != nil {
		return 0
	}
	n := 0
	for _, e := range out.Data {
		switch e.Status {
		case "success", "error", "crashed", "canceled":
			n++
		}
	}
	return n
}

// latestExecError copies the error of the workflow's newest execution, if it failed.
func latestExecError(ctx context.Context, cl *n8nClient, wfID string, sr *StepResult) error {
	execs, err := cl.executions(ctx, wfID, 1)
	if err != nil || len(execs) == 0 {
		return err
	}
	rd := execs[0].Data.ResultData
	sr.Failed = execs[0].Status == "error" || execs[0].Status == "crashed"
	if rd.Error != nil {
		sr.ExecError = strings.TrimSpace(rd.Error.Message + " " + rd.Error.Description)
		if rd.Error.Node != nil {
			sr.ErrorNode = rd.Error.Node.Name
		}
		return nil
	}
	for name, runs := range rd.RunData {
		for _, r := range runs {
			if r.Error != nil {
				sr.ExecError = strings.TrimSpace(r.Error.Message + " " + r.Error.Description)
				sr.ErrorNode = name
			}
		}
	}
	return nil
}

func mockPut(ctx context.Context, admin, tenant string, seed *mock.Seed) error {
	b, _ := json.Marshal(seed)
	req, _ := http.NewRequestWithContext(ctx, "PUT", admin+"/admin/tenants/"+url.PathEscape(tenant), bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("seed refused: %s", msg)
	}
	return nil
}

func mockGet(ctx context.Context, admin, tenant string) (*mock.Snapshot, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", admin+"/admin/tenants/"+url.PathEscape(tenant), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var s mock.Snapshot
	return &s, json.NewDecoder(resp.Body).Decode(&s)
}

func mockDelete(admin, tenant string) {
	req, _ := http.NewRequest("DELETE", admin+"/admin/tenants/"+url.PathEscape(tenant), nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}
