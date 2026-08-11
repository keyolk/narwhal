//go:build unix

package main

import "syscall"

// detachedProcAttr puts the child in its own session so it survives signals
// sent to the parent's process group and terminal hangups.
func detachedProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
