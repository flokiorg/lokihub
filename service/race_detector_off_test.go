//go:build !race

package service

// raceDetectorEnabled is false in an ordinary build. See race_detector_on_test.go.
const raceDetectorEnabled = false
