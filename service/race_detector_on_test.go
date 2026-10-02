//go:build race

package service

// raceDetectorEnabled is true only in a -race build.
//
// Go exposes no runtime check for this, so it takes a build-tag pair. Used by the
// throughput measurements in auditD_secb_*_test.go, which derive an achievable
// events-per-second rate from wall-clock time: the detector slows execution by
// roughly 5-20x, so the figure measured under it is not the figure being reasoned
// about, and an assertion against a fixed capacity fails for a reason that has
// nothing to do with the property under test.
const raceDetectorEnabled = true
