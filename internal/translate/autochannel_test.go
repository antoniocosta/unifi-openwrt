// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"strings"
	"testing"
)

// A channel the daemon chose replaces "auto" while the htmode is the one it
// was chosen for; a channel set in the controller always wins.
func TestPlanAutoChannel(t *testing.T) {
	cur := UCI{
		"wireless.radio1": "wifi-device", "wireless.radio1.band": "5g",
		"wireless.radio1.channel": "auto", "wireless.radio1.htmode": "HT40", "wireless.radio1.disabled": "0",
	}
	cfg := Cfg{"radio.1.phyname": "wifi1", "radio.1.ieee_mode": "11naht40", "radio.1.channel": "auto", "radio.1.status": "enabled"}
	channel := func(r Result) string {
		for _, o := range r.Ops {
			if o.Key == "wireless.radio1.channel" {
				return o.Value
			}
		}
		return ""
	}
	note := func(r Result) bool {
		return strings.Contains(strings.Join(r.Notes, "\n"), "chosen by the daemon")
	}

	if r := Plan(cfg, cur, Features{}); channel(r) != "" || note(r) {
		t.Errorf("no choice: channel op %q, notes %q", channel(r), r.Notes)
	}
	f := Features{AutoChannel: map[string]AutoChannel{"radio1": {Channel: 36, HTMode: "HT40"}}}
	if r := Plan(cfg, cur, f); channel(r) != "36" || !note(r) {
		t.Errorf("chosen 36: channel op %q, notes %q", channel(r), r.Notes)
	}
	// already on it: nothing to change, the note stays
	cur["wireless.radio1.channel"] = "36"
	if r := Plan(cfg, cur, f); channel(r) != "" || !note(r) {
		t.Errorf("on 36 already: channel op %q, notes %q", channel(r), r.Notes)
	}
	// chosen for another width: back to auto
	f.AutoChannel["radio1"] = AutoChannel{Channel: 165, HTMode: "HT20"}
	if r := Plan(cfg, cur, f); channel(r) != "auto" || note(r) {
		t.Errorf("chosen for HT20: channel op %q, notes %q", channel(r), r.Notes)
	}
	// the controller names a channel
	f.AutoChannel["radio1"] = AutoChannel{Channel: 36, HTMode: "HT40"}
	cfg["radio.1.channel"] = "149"
	if r := Plan(cfg, cur, f); channel(r) != "149" || note(r) {
		t.Errorf("controller says 149: channel op %q, notes %q", channel(r), r.Notes)
	}
}
