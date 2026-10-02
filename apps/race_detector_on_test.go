//go:build race

package apps_test

// raceDetectorEnabled is true only in a -race build. Go exposes no runtime check,
// so it takes a build-tag pair. See raceTrials.
const raceDetectorEnabled = true
