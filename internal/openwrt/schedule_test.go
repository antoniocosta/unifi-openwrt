// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"strings"
	"testing"

	"unifi-openwrt/internal/translate"
)

func TestScheduleConf(t *testing.T) {
	c, err := scheduleConf([]translate.Schedule{{Section: "unifi_radio0_12", OnDuring: true, Windows: map[string][]string{"mon": {"0800-0900"}, "sun": {"2200-0200"}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := "unifi_radio0_12 on - 0 0\nunifi_radio0_12 on mon 0800 0900\nunifi_radio0_12 on sun 2200 0200\n"
	if c != want {
		t.Fatalf("got\n%s", c)
	}
	if _, err := scheduleConf([]translate.Schedule{{Section: "x; reboot"}}); err == nil {
		t.Fatal("bad section accepted")
	}
	if !strings.Contains(scheduleScript, "wifi reload") {
		t.Fatal("script")
	}
}
