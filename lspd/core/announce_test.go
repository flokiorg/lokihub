package core

import (
	"errors"
	"testing"
)

func TestNextAnnounceStatus(t *testing.T) {
	cases := []struct {
		from   AnnounceStatus
		action AnnounceAction
		want   AnnounceStatus
		err    bool
	}{
		{AnnounceOpen, AnnouncePause, AnnouncePaused, false},
		{AnnouncePaused, AnnouncePause, AnnouncePaused, false},
		{AnnounceClosed, AnnouncePause, "", true},
		{AnnounceOpen, AnnounceResume, AnnounceOpen, false},
		{AnnouncePaused, AnnounceResume, AnnounceOpen, false},
		{AnnounceClosed, AnnounceResume, AnnounceOpen, false},
		{AnnounceOpen, AnnounceUnlist, AnnounceClosed, false},
		{AnnouncePaused, AnnounceUnlist, AnnounceClosed, false},
		{AnnounceClosed, AnnounceUnlist, AnnounceClosed, false},
		{AnnounceOpen, "delete", "", true},
		{"bogus", AnnounceResume, "", true},
	}
	for _, tc := range cases {
		got, err := NextAnnounceStatus(tc.from, tc.action)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("%s + %s = %q, %v; want %q, err=%v", tc.from, tc.action, got, err, tc.want, tc.err)
		}
	}
	if _, err := NextAnnounceStatus(AnnounceClosed, AnnouncePause); !errors.Is(err, ErrAnnounceTransition) {
		t.Fatalf("err = %v, want ErrAnnounceTransition", err)
	}
}
