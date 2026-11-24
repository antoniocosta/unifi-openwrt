// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package main

import (
	"net"
	"testing"
)

func TestARPProbe(t *testing.T) {
	mac := net.HardwareAddr{0x02, 0, 0, 0, 0, 1}
	gw := net.IPv4(192, 0, 2, 1).To4()
	req := arpProbe(mac, gw)
	if len(req) != 42 || req[21] != 1 || string(req[28:32]) != "\x00\x00\x00\x00" || !net.IP(req[38:42]).Equal(gw) {
		t.Fatalf("request % x", req)
	}
	// the gateway's answer: its MAC and address as sender, ours and 0.0.0.0 as target
	ans := make([]byte, 60)
	copy(ans, req)
	gmac := []byte{0x02, 0, 0, 0, 0, 9}
	copy(ans[0:6], mac)
	copy(ans[6:12], gmac)
	ans[21] = 2
	copy(ans[22:28], gmac)
	copy(ans[28:32], gw)
	copy(ans[32:38], mac)
	copy(ans[38:42], []byte{0, 0, 0, 0})
	macs := [][]byte{mac}
	if !arpProbeAnswer(ans, gw, macs) {
		t.Error("the answer was not recognised")
	}
	// an answer to the kernel's own request names our address as target
	own := append([]byte{}, ans...)
	copy(own[38:42], []byte{192, 0, 2, 7})
	if arpProbeAnswer(own, gw, macs) {
		t.Error("an answer to another request was taken")
	}
	other := append([]byte{}, ans...)
	copy(other[28:32], []byte{192, 0, 2, 2})
	if arpProbeAnswer(other, gw, macs) || arpProbeAnswer(req, gw, macs) || arpProbeAnswer(ans[:30], gw, macs) {
		t.Error("another host's answer, a request or a short frame was taken")
	}
}
