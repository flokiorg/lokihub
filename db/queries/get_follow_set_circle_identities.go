package queries

import (
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

// GetFollowSetCircleIdentities returns a page of CircleIdentity rows using
// the "follow_set" policy, ordered by id for stable pagination across ticks.
// Since multiple circle_hub apps may share one CircleIdentity, this naturally
// dedupes refresh work: one relay query per unique identity, not per provider.
func GetFollowSetCircleIdentities(gormDB *gorm.DB, limit, offset int) ([]db.CircleIdentity, error) {
	var identities []db.CircleIdentity
	err := gormDB.Where("policy = ?", db.CirclePolicyFollowSet).
		Order("id asc").
		Limit(limit).Offset(offset).
		Find(&identities).Error
	return identities, err
}
