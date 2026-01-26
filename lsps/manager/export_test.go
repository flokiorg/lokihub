package manager

import "context"

func (m *LiquidityManager) ProcessInternalEventsForTest(ctx context.Context) {
	m.processInternalEvents(ctx)
}
