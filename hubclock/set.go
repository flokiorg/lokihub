package hubclock

func setOffset(secs int64) { offsetSecs.Store(secs) }
