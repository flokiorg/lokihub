package queries

import (
	"github.com/flokiorg/lokihub/db"
	"gorm.io/gorm"
)

// GetFollowingCircleIdentities returns a page of CircleIdentity rows using
// "following" policy, ordered by id for stable pagination across ticks. Since
// multiple circle_hub apps may share one CircleIdentity, this naturally
// dedupes refresh work: one relay query per unique identity, not per provider.
func GetFollowingCircleIdentities(gormDB *gorm.DB, limit, offset int) ([]db.CircleIdentity, error) {
	var identities []db.CircleIdentity
	err := gormDB.Where("policy = ?", db.CirclePolicyFollowing).
		Order("id asc").
		Limit(limit).Offset(offset).
		Find(&identities).Error
	return identities, err
}
