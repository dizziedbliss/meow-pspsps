package runner

import (
	"encoding/json"
	"testing"
)

func TestIsStartedAck(t *testing.T) {
	yes := []string{`{"message":"Workflow was started"}`, ` {"message": "Workflow was started"} `}
	no := []string{``, `{"message":"Workflow was started","x":1}`, `{"status":"accepted"}`, `Workflow was started`, `{"message":"Thanks"}`}
	for _, b := range yes {
		if !isStartedAck(b) {
			t.Errorf("should be an ack: %q", b)
		}
	}
	for _, b := range no {
		if isStartedAck(b) {
			t.Errorf("should not be an ack: %q", b)
		}
	}
}

const cannedExec = `{
 "id": "12", "status": "success",
 "data": {"resultData": {
   "lastNodeExecuted": "Reply Accepted",
   "runData": {
     "Webhook": [{"data": {"main": [[{"json": {"body": {"roll_number": "R1"}}}]]}}],
     "Reply Accepted": [{"data": {"main": [[{"json": {"status": "accepted", "message": "ok"}}, {"json": {"status": "second"}}]]}}]
   }}}}`

func TestLastNodeOutput(t *testing.T) {
	var e execution
	if err := json.Unmarshal([]byte(cannedExec), &e); err != nil {
		t.Fatal(err)
	}
	got, ok := lastNodeOutput(&e)
	if !ok || got != `{"message":"ok","status":"accepted"}` {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	// a crashed execution has no usable output
	var bad execution
	_ = json.Unmarshal([]byte(`{"status":"error","data":{"resultData":{"error":{"message":"boom"},"lastNodeExecuted":"X","runData":{}}}}`), &bad)
	if _, ok := lastNodeOutput(&bad); ok {
		t.Fatal("an errored execution must not yield an answer")
	}
	// no items on the last node
	var empty execution
	_ = json.Unmarshal([]byte(`{"status":"success","data":{"resultData":{"lastNodeExecuted":"X","runData":{"X":[{"data":{"main":[[]]}}]}}}}`), &empty)
	if _, ok := lastNodeOutput(&empty); ok {
		t.Fatal("no output items must not yield an answer")
	}
}
