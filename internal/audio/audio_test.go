package audio

import "testing"

func TestMetricsResetAndGet(t *testing.T) {
	ResetMetrics()
	got := GetMetrics()
	if got != (Metrics{}) {
		t.Fatalf("metrics after reset = %+v, want zero value", got)
	}
}
