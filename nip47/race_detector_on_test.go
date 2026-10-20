//go:build race

package nip47

// raceDetectorEnabled is true only in a -race build. The detector slows the two
// arms of a timing comparison by different amounts, so wall-clock skew measured
// under it says nothing about the property under test.
const raceDetectorEnabled = true
