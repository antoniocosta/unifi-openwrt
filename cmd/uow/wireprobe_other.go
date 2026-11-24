// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !linux

package main

// wireProbe needs Linux packet sockets (wireprobe_linux.go). Exit 2 = cannot
// tell.
func wireProbe(args []string) int { return 2 }
