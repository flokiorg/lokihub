// Copyright (c) 2024 The Flokicoin developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or http://www.opensource.org/licenses/mit-license.php.

//go:build !windows

// Package crashlog provides OS-level file descriptor redirection helpers to capture panics.
package crashlog

import (
	"os"

	"golang.org/x/sys/unix"
)

// RedirectStderr redirects the process stderr (fd 2) to the given file so that
// unhandled panics and runtime fatals are persisted to disk.
func RedirectStderr(f *os.File) error {
	return unix.Dup2(int(f.Fd()), int(os.Stderr.Fd()))
}
