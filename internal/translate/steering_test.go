// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "testing"

func TestBandSteering(t *testing.T) {
	c := ParseCfg("bandsteering.status=enabled\nbandsteering.mode=prefer_5g\nbandsteering.1.status=enabled\nbandsteering.1.vap.1.devname=wifi0ap0\nbandsteering.1.vap.2.devname=wifi1ap2\n")
	s := BandSteering(c)
	if s.Mode != "prefer_5g" || len(s.Devnames) != 2 {
		t.Fatalf("%+v", s)
	}
	if s := BandSteering(ParseCfg("bandsteering.status=disabled\n")); s.Mode != "" {
		t.Fatalf("off: %+v", s)
	}
}
