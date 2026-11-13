// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"strings"
	"testing"
)

func TestQoSRuleset(t *testing.T) {
	rs, err := qosRuleset([]IfLimit{{"phy0-ap2", 5000, 2000}, {"phy1-ap2", 0, 1000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`oifname "phy0-ap2" update @dl_0 { ether daddr limit rate over 625 kbytes/second`, `iifname "phy0-ap2" update @ul_0 { ether saddr limit rate over 250 kbytes/second`, `iifname "phy1-ap2" update @ul_1`} {
		if !strings.Contains(rs, w) {
			t.Errorf("missing %q in\n%s", w, rs)
		}
	}
	if strings.Contains(rs, "@dl_1") {
		t.Error("no download limit expected on phy1-ap2")
	}
}
