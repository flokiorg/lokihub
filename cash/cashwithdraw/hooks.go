package cashwithdraw

import "fmt"

// Stages of the withdraw commit (W6) and of the re-mint commit at which
// TestHooks.Before runs (plan §4.2).
const (
	StageAfterLock          = "AfterLock"
	StageAfterRecheck       = "AfterRecheck"
	StageAfterSourceClaim   = "AfterSourceClaim"
	StageAfterDebit         = "AfterDebit"
	StageAfterHold          = "AfterHold"
	StageAfterSetAside      = "AfterSetAside"
	StageAfterProof         = "AfterProof"
	StageAfterReceiptSign   = "AfterReceiptSign"
	StageAfterRecord        = "AfterRecord"
	StageBeforeCommit       = "BeforeCommit"
	StageAfterCreateApp     = "AfterCreateApp"
	StageAfterSign          = "AfterSign"
	StageAfterClaims        = "AfterClaims"
	StageAfterCredit        = "AfterCredit"
	StageAfterSourceRewrite = "AfterSourceRewrite"
)

// CommitStages and RemintStages are every stage of each commit, in order.
var (
	CommitStages = []string{StageAfterLock, StageAfterRecheck, StageAfterSourceClaim, StageAfterDebit,
		StageAfterHold, StageAfterSetAside, StageAfterProof, StageAfterReceiptSign, StageAfterRecord, StageBeforeCommit}
	RemintStages = []string{StageAfterLock, StageAfterCreateApp, StageAfterSign, StageAfterClaims,
		StageAfterCredit, StageAfterSourceRewrite, StageAfterRecord, StageBeforeCommit}
)

// TestHooks is the fault-injection seam: Before runs at every stage, an error
// rolls the commit back, a panic abandons it as a crash would. The remint
// commit's stages are reported as "Remint:<stage>". nil in production.
type TestHooks struct {
	Before func(stage string) error
}

func (s *Service) before(stage string) error {
	if s.Hooks == nil || s.Hooks.Before == nil {
		return nil
	}
	return s.Hooks.Before(stage)
}

func (s *Service) beforeRemint(stage string) error {
	return s.before(fmt.Sprintf("Remint:%s", stage))
}
