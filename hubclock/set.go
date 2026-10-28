package hubclock

func setOffset(secs int64) {
	offsetSecs.Store(secs)
	movedMu.Lock()
	close(moved)
	moved = make(chan struct{})
	movedMu.Unlock()
}
