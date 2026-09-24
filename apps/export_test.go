package apps

// Test-only handles on the two union queries, so the derivation guard in
// cash_hub_stats_all_test.go can compare them from the external test package
// without exporting either into the real API.
const (
	ExportedCashClaimUnionSQL = cashClaimUnionSQL
)

var ExportedAllHubsCashClaimUnionSQL = allHubsCashClaimUnionSQL
