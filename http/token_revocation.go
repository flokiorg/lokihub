package http

import (
	"time"

	"gorm.io/gorm"

	"github.com/flokiorg/lokihub/db"
	"github.com/flokiorg/lokihub/logger"
)

// tokenRevocation is the server-side denylist that makes logout and
// single-token revocation real: a stateless HS256 JWT cannot otherwise be
// invalidated before its ExpiresAt. Keyed by the token's jti.
type tokenRevocation interface {
	// IsRevoked reports whether the jti has been revoked. A store that cannot
	// answer says true, so the request is rejected: fail closed, mirroring the
	// served-nonce store (service/private_served_nonces.go).
	IsRevoked(jti string) bool
	// Revoke records the jti as revoked until expiresAt (the token's own
	// ExpiresAt, after which it is refused on expiry anyway). Idempotent.
	Revoke(jti string, expiresAt int64) error
}

// dbTokenRevocation keeps revoked jtis in revoked_tokens, so a logout survives
// a restart for as long as the revoked token would otherwise have remained
// valid.
type dbTokenRevocation struct{ db *gorm.DB }

func (s dbTokenRevocation) IsRevoked(jti string) bool {
	var n int64
	if err := s.db.Model(&db.RevokedToken{}).Where("jti = ?", jti).Count(&n).Error; err != nil {
		logger.Logger.Warn().Err(err).Msg("Could not read the revoked-token table; rejecting the request")
		return true
	}
	return n > 0
}

// Revoke prunes any entries whose tokens have already expired, then records the
// jti. A duplicate (already revoked) is not an error.
func (s dbTokenRevocation) Revoke(jti string, expiresAt int64) error {
	if _, err := s.prune(time.Now()); err != nil {
		logger.Logger.Warn().Err(err).Msg("Failed to prune expired revoked tokens")
	}
	err := s.db.Create(&db.RevokedToken{JTI: jti, ExpiresAt: expiresAt, RevokedAt: time.Now().Unix()}).Error
	if err != nil && db.IsDuplicateKeyError(err) {
		return nil
	}
	return err
}

// prune forgets every jti whose token has already expired; such a token is
// refused on expiry regardless, so the denylist need not keep remembering it.
func (s dbTokenRevocation) prune(now time.Time) (int64, error) {
	result := s.db.Where("expires_at < ?", now.Unix()).Delete(&db.RevokedToken{})
	return result.RowsAffected, result.Error
}
