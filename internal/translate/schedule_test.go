// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
	"testing"
)

func TestWLANSchedule(t *testing.T) {
	c := ParseCfg("wireless.6.schedule_enabled=enabled\nwireless.6.schedule_invert=enabled\nwireless.6.schedule_mon.0=0800-0900\nwireless.6.schedule_mon.1=1800-1900\nwireless.6.schedule_sun.0=bad\n")
	s := wlanSchedule(c, "wireless.6.", "sec")
	if s == nil || !s.OnDuring || !reflect.DeepEqual(s.Windows["mon"], []string{"0800-0900", "1800-1900"}) || len(s.Windows["sun"]) != 0 {
		t.Fatalf("%+v", s)
	}
	if wlanSchedule(ParseCfg("wireless.1.schedule_enabled=disabled\n"), "wireless.1.", "x") != nil {
		t.Fatal("disabled schedule")
	}
}
