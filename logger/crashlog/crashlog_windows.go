// Copyright (c) 2024 The Flokicoin developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or http://www.opensource.org/licenses/mit-license.php.

//go:build windows

// Package crashlog provides OS-level file descriptor redirection helpers to capture panics.
package crashlog

import (
	"os"

	"golang.org/x/sys/windows"
)

// RedirectStderr redirects the process stderr to the given file using the Windows
// SetStdHandle API so that unhandled panics and runtime fatals are persisted to disk.
func RedirectStderr(f *os.File) error {
	return windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
}
