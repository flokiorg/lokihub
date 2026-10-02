//go:build !race

package apps_test

// raceDetectorEnabled is false in an ordinary build. See race_detector_on_test.go.
const raceDetectorEnabled = false
