package web

import (
	"testing"
	"time"

	"github.com/jsixface/godexvert/internal/convert"
)

func TestETA(t *testing.T) {
	j := convert.Job{Status: convert.InProgress, Progress: 25, RunStart: time.Now().Add(-60 * time.Second)}
	if got := eta(j); got != "~03:00 left" {
		t.Errorf("eta = %q", got)
	}
	for _, j := range []convert.Job{
		{Status: convert.InProgress, Progress: 0, RunStart: time.Now()},
		{Status: convert.Queued, Progress: 50, RunStart: time.Now()},
		{Status: convert.InProgress, Progress: 50},
	} {
		if got := eta(j); got != "" {
			t.Errorf("eta(%+v) = %q, want empty", j, got)
		}
	}
}
