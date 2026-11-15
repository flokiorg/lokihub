package mocks

import (
	"github.com/flokiorg/lokihub/config"
	"github.com/flokiorg/lokihub/keys"
	"github.com/flokiorg/lokihub/lnclient"
)

// Compile-time proof that each mock still satisfies the interface it stands in
// for. Without these, a mock that falls behind its interface fails at RUN time
// and only in whichever test happens to pass it through a type assertion.
//
// This is not hypothetical. Adding Keys.GetPrivateTransportKey without updating
// MockKeys produced exactly that: `go build` passed, `go vet` passed, and
// http's TestUnlock_IncorrectPassword died with
//
//	panic: *mocks.MockKeys is not keys.Keys: missing method GetPrivateTransportKey
//
// which points at the symptom rather than the cause. Worse, the test that broke
// is unrelated to keys, so the failure gave no hint about what had changed.
//
// These mocks are hand-maintained — the repo has no mockery binary and no
// .mockery.yaml despite the generated headers — so nothing else enforces that
// they keep up. A one-line assertion per mock makes the compiler do it, and the
// error then names the missing method at the point of the mock rather than deep
// inside an unrelated test.
//
// Add a line here whenever a mock is added.
var (
	_ config.Config     = (*MockConfig)(nil)
	_ keys.Keys         = (*MockKeys)(nil)
	_ lnclient.LNClient = (*MockLNClient)(nil)
)
