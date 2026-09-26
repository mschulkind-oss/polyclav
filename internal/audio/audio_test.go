package audio

import "testing"

func TestMetricsResetAndGet(t *testing.T) {
	ResetMetrics()
	got := GetMetrics()
	if got != (Metrics{}) {
		t.Fatalf("metrics after reset = %+v, want zero value", got)
	}
}

func TestSetClapPluginMissingPathFailsBeforeFFI(t *testing.T) {
	missing := t.TempDir() + "/missing.clap"
	err := SetClapPlugin(missing, "com.test")
	if err == nil {
		t.Fatal("SetClapPlugin missing path succeeded")
	}
}
