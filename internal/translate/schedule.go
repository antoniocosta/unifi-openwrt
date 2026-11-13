// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"regexp"
	"sort"
	"strconv"
)

// Schedule is a WLAN's broadcast schedule on one AP interface: the AP turns
// the interface on during the windows (Invert: the controller's
// schedule_invert=enabled, "on during") or off during them.
type Schedule struct {
	Section  string              // UCI wifi-iface section
	OnDuring bool                // true: up only in the windows
	Windows  map[string][]string // day (mon..sun) -> "HHMM-HHMM"
}

var window = regexp.MustCompile(`^[0-2][0-9][0-5][0-9]-[0-2][0-9][0-5][0-9]$`)

var days = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// wlanSchedule reads wireless.N.schedule_* (nil when not scheduled).
func wlanSchedule(cfg Cfg, w, section string) *Schedule {
	if cfg[w+"schedule_enabled"] != "enabled" {
		return nil
	}
	s := &Schedule{Section: section, OnDuring: cfg[w+"schedule_invert"] == "enabled", Windows: map[string][]string{}}
	for _, d := range days {
		for i := 0; i < 32; i++ { // schedule_<day>.<i> (leaf keys)
			if v := cfg[w+"schedule_"+d+"."+strconv.Itoa(i)]; window.MatchString(v) {
				s.Windows[d] = append(s.Windows[d], v)
			}
		}
		sort.Strings(s.Windows[d])
	}
	return s
}
