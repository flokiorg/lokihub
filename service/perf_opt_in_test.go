package service

import (
	"os"
	"testing"
)

// requirePerfOptIn skips wall-clock throughput measurements unless LOKIHUB_PERF is
// set. They assert a rate this hardware can reach, which a shared or loaded
// machine fails for reasons unrelated to the code: run them on real hardware with
// `LOKIHUB_PERF=1 go test -run TestAuditDSecB ./service/`.
func requirePerfOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("LOKIHUB_PERF") == "" {
		t.Skip("throughput measurement: set LOKIHUB_PERF=1 to run it on real hardware")
	}
}
