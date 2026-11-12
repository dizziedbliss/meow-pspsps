package runner

import (
	"context"
	"testing"
	"time"
)

func TestWaitExecutionFinished(t *testing.T) {
	if !waitExecutionFinished(context.Background(), time.Second, func() bool { return true }) {
		t.Fatal("completed execution reported unfinished")
	}
	if waitExecutionFinished(context.Background(), time.Millisecond, func() bool { return false }) {
		t.Fatal("unfinished execution reported completed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitExecutionFinished(ctx, time.Second, func() bool { return false }) {
		t.Fatal("cancelled execution reported completed")
	}
}
