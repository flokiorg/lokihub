package common

// TimeFormat is the layout used for timestamp fields in LSPS
// protocol/wire representations (e.g. OpeningFeeParams.ValidUntil), not
// for log output — see logger.go for that.
const TimeFormat = "2006-01-02T15:04:05.999Z"
