// Package sliceutil holds generic slice helpers.
package sliceutil

// Filter returns the elements of s for which f is true (nil when none are).
func Filter[T any](s []T, f func(T) bool) []T {
	var r []T
	for _, v := range s {
		if f(v) {
			r = append(r, v)
		}
	}
	return r
}
