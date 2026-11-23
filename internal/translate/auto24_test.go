// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "testing"

// Automatic channel on 2.4 GHz is kept to 1, 6 and 11; a channel from the
// controller takes the list away again.
func TestPlanAuto24Channels(t *testing.T) {
	cur := UCI{"wireless.radio0": "wifi-device", "wireless.radio0.band": "2g", "wireless.radio0.channel": "auto", "wireless.radio0.htmode": "HT20", "wireless.radio0.disabled": "0"}
	cfg := Cfg{"radio.1.phyname": "wifi0", "radio.1.ieee_mode": "11nght20", "radio.1.channel": "auto", "radio.1.status": "enabled"}
	find := func(r Result) (string, bool) {
		for _, o := range r.Ops {
			if o.Key == "wireless.radio0.channels" {
				return o.Value, o.Del
			}
		}
		return "", false
	}
	if v, del := find(Plan(cfg, cur, Features{})); v != "1 6 11" || del {
		t.Errorf("auto: channels %q del=%v", v, del)
	}
	cur["wireless.radio0.channels"] = "1 6 11"
	if v, del := find(Plan(cfg, cur, Features{})); v != "" || del {
		t.Errorf("already set: channels %q del=%v", v, del)
	}
	cfg["radio.1.channel"] = "6"
	if _, del := find(Plan(cfg, cur, Features{})); !del {
		t.Error("a fixed channel must remove the list")
	}
}
