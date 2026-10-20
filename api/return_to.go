package api

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
)

// unsafeReturnToSchemes run or show content inside the hub's own page when the
// browser opens them, instead of handing off to another app or site.
var unsafeReturnToSchemes = []string{"javascript", "vbscript", "data", "blob", "file", "about"}

// parseReturnTo parses the link the hub opens once an app is connected. It comes
// from the link that started the connection, so it must name another app or
// site: one that runs in the hub's page would run with the user's session.
// Returns nil for an empty link.
func parseReturnTo(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid returnTo: %w", err)
	}
	// url.Parse lowercases the scheme, so "JavaScript:" is caught here too.
	if u.Scheme == "" || slices.Contains(unsafeReturnToSchemes, u.Scheme) {
		return nil, errors.New("invalid returnTo: must be a link to another app or site")
	}
	return u, nil
}
