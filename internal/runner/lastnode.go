package runner

import (
	"context"
	"encoding/json"
	"strings"
)

// startedAck is what n8n's Webhook node answers when it is set to respond immediately
// ("onReceived") and sends no custom data: {"message":"Workflow was started"} (see
// webhook-on-received-response-extractor.js in n8n 2.47.4).
const startedAck = "Workflow was started"

// isStartedAck reports whether a webhook reply is only n8n's "workflow started" acknowledgement.
func isStartedAck(body string) bool {
	var m map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &m) != nil || len(m) != 1 {
		return false
	}
	msg, _ := m["message"].(string)
	return msg == startedAck
}

// lastNodeOutput returns the first output item of the last node a finished execution ran, as JSON.
func lastNodeOutput(e *execution) (string, bool) {
	rd := e.Data.ResultData
	if rd.Error != nil || rd.LastNodeExecuted == "" {
		return "", false
	}
	runs := rd.RunData[rd.LastNodeExecuted]
	if len(runs) == 0 {
		return "", false
	}
	main := runs[len(runs)-1].Data.Main
	if len(main) == 0 || len(main[0]) == 0 {
		return "", false
	}
	b, err := json.Marshal(main[0][0].JSON)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// useLastNodeIfAcknowledged replaces an "immediate response" acknowledgement with the last node's
// output from the workflow's newest execution (each test has its own workflow copy and steps run
// one at a time, so the newest finished execution is this step's). It reports whether it did.
func useLastNodeIfAcknowledged(ctx context.Context, cl *n8nClient, wfID string, sr *StepResult) bool {
	if sr.Status/100 != 2 || !isStartedAck(sr.Body) {
		return false
	}
	execs, err := cl.executions(ctx, wfID, 1)
	if err != nil || len(execs) == 0 {
		return false
	}
	out, ok := lastNodeOutput(&execs[0])
	if !ok {
		return false
	}
	sr.Body, sr.ViaLastNode = out, true
	return true
}
