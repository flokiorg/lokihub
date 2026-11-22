package http

import (
	"errors"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/mcp"
)

const (
	mcpExecutionTTL        = 24 * time.Hour // the idempotency window
	mcpActivityKeepDays    = 90             // durable activity retention
	mcpActivityPerApp      = 5000           // and at most this many per assistant
	mcpActivityFailsPerApp = 500            // failed/refused calls, kept separately
	mcpPruneEvery          = 200            // writes between prunes
)

// gormExecutions persists completed assistant writes (critic O1).
type gormExecutions struct {
	db *gorm.DB
	n  atomic.Uint64
}

func (g *gormExecutions) Get(appID uint, tool, key string) (string, []byte, bool, error) {
	var row db.MCPExecution
	err := g.db.Where("app_id = ? AND tool = ? AND key = ? AND created_at > ?", appID, tool, key, time.Now().Add(-mcpExecutionTTL)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return row.ArgsHash, []byte(row.Result), true, nil
}

func (g *gormExecutions) Put(appID uint, tool, key, argsHash string, result []byte) error {
	if g.n.Add(1)%mcpPruneEvery == 0 {
		g.db.Where("created_at < ?", time.Now().Add(-mcpExecutionTTL)).Delete(&db.MCPExecution{})
	}
	// Replace an expired row with the same key (the unique index ignores
	// expiry), so a reused key is always persisted (pentest R2 L2).
	g.db.Where("app_id = ? AND tool = ? AND key = ? AND created_at <= ?", appID, tool, key, time.Now().Add(-mcpExecutionTTL)).Delete(&db.MCPExecution{})
	return g.db.Create(&db.MCPExecution{AppID: appID, Tool: tool, Key: key, ArgsHash: argsHash, Result: string(result)}).Error
}

// gormActivity persists the owner's activity log.
type gormActivity struct {
	db *gorm.DB
	n  atomic.Uint64
}

func (g *gormActivity) Save(e mcp.ActivityEntry) error {
	if g.n.Add(1)%mcpPruneEvery == 0 {
		g.prune(e.AppID)
	}
	return g.db.Create(&db.MCPActivity{AppID: e.AppID, Tool: e.Tool, Outcome: e.Outcome, Detail: e.Detail, CreatedAt: e.Time}).Error
}

// prune keeps 90 days and, per assistant, the newest entries of each class
// separately — failures can never push out real activity (pentest R2 M3).
func (g *gormActivity) prune(appID uint) {
	g.db.Where("created_at < ?", time.Now().AddDate(0, 0, -mcpActivityKeepDays)).Delete(&db.MCPActivity{})
	for _, c := range []struct {
		cond string
		keep int
	}{{"outcome = 'failed'", mcpActivityFailsPerApp}, {"outcome != 'failed'", mcpActivityPerApp}} {
		var cutoff db.MCPActivity
		if err := g.db.Where("app_id = ? AND "+c.cond, appID).Order("id desc").Offset(c.keep).First(&cutoff).Error; err == nil {
			g.db.Where("app_id = ? AND "+c.cond+" AND id <= ?", appID, cutoff.ID).Delete(&db.MCPActivity{})
		}
	}
}

func (g *gormActivity) Recent(appID uint, limit int) ([]mcp.ActivityEntry, error) {
	q := g.db.Order("id desc").Limit(limit)
	if appID != 0 {
		q = q.Where("app_id = ?", appID)
	}
	var rows []db.MCPActivity
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]mcp.ActivityEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, mcp.ActivityEntry{Time: r.CreatedAt, AppID: r.AppID, Tool: r.Tool, Outcome: r.Outcome, Detail: r.Detail})
	}
	return out, nil
}

// gormSpends persists the owner's 24h spending window (pentest R3-A).
type gormSpends struct{ db *gorm.DB }

func (g *gormSpends) Add(amt uint64, at time.Time) (uint, error) {
	g.db.Where("created_at < ?", time.Now().Add(-48*time.Hour)).Delete(&db.MCPSpend{})
	row := db.MCPSpend{AmountMloki: amt, CreatedAt: at}
	err := g.db.Create(&row).Error
	return row.ID, err
}

func (g *gormSpends) Remove(id uint) error { return g.db.Delete(&db.MCPSpend{}, id).Error }

func (g *gormSpends) Since(t time.Time) ([]mcp.SpendRecord, error) {
	var rows []db.MCPSpend
	if err := g.db.Where("created_at >= ?", t).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]mcp.SpendRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, mcp.SpendRecord{ID: r.ID, AmountMloki: r.AmountMloki, At: r.CreatedAt})
	}
	return out, nil
}
