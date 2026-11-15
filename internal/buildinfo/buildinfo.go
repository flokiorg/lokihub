// Package buildinfo carries values stamped into the binary at link time.
//
// Tag is the release version, set with
//
//	-ldflags "-X 'github.com/flokiorg/lokihub/internal/buildinfo.Tag=<version>'"
//
// It is empty in an unstamped build.
package buildinfo

// Tag is the release version stamped at link time.
var Tag string = ""
