package apps_test

// raceTrials scales a concurrency test's trial count down under -race.
//
// This package's race tests each spin up a service, build a hub and a wallet, then
// drive two goroutines at one slice — eight such loops at 100-200 trials. Plain
// they total well under a minute; under the race detector the package took ~9m
// locally and then exceeded CI's default 10m per-package timeout, which is a hard
// failure of the required check rather than a slow build.
//
// Dividing by ten is a deliberate trade, not a dodge. These loops exist to catch a
// rare interleaving by repetition, and the detector already perturbs scheduling, so
// the interleavings explored under it are not the ones a plain run explores — 200
// repetitions of a differently-shaped race buy far less than 200 of the real one.
// The full count still runs in the ordinary suite, which is where the assertion
// earns its keep; under -race the point is that the invariants hold at all while
// the detector watches for actual data races.
//
// Floored at 10 so a reduced run is still a repetition test and not a single shot.
func raceTrials(n int) int {
	if !raceDetectorEnabled {
		return n
	}
	if r := n / 10; r > 10 {
		return r
	}
	return 10
}
