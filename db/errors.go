package db

import (
	"errors"

	"gorm.io/gorm"
)

// sqlite extended result codes for a unique or primary-key violation
// (https://www.sqlite.org/rescode.html).
const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

// IsDuplicateKeyError reports whether err is a unique-constraint violation on
// either backend.
//
// gorm's TranslateError turns postgres's into gorm.ErrDuplicatedKey, but its
// sqlite translator reads the error code from the JSON form of a mattn/go-sqlite3
// error; the modernc driver lokihub uses keeps its code unexported, so on sqlite
// the violation arrives untranslated and is recognised here by its Code().
func IsDuplicateKeyError(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		switch coded.Code() {
		case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
			return true
		}
	}
	return false
}
