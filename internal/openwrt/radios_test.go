// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"reflect"
	"testing"
	"time"
)

// network.wireless status gives every radio its band and wifi-device: a
// 5 GHz interface waiting on DFS (frequency 0) stays 5 GHz, and a radio with
// no interface up is still reported.
func TestRadiosFromWirelessStatus(t *testing.T) {
	out := `=== board
{"hostname":"ap","model":"m","release":{"description":"OpenWrt"}}
=== iface phy1-ap0 phy1
{"ssid":"x","bssid":"02:00:00:00:00:01","mode":"Master","channel":11,"frequency":2462}
=== assoc phy1-ap0
{"results":[]}
=== iface phy2-ap0 phy2
{"ssid":"x","bssid":"02:00:00:00:00:02","mode":"Master","channel":0,"frequency":0}
=== assoc phy2-ap0
{"results":[]}
=== wstatus
{"radio0":{"up":false,"disabled":false,"config":{"band":"5g","channel":"auto","htmode":"HE80"},"interfaces":[{"section":"default_radio0"}]},
 "radio1":{"up":true,"disabled":false,"config":{"band":"2g","channel":"11","htmode":"HE20"},"interfaces":[{"ifname":"phy1-ap0"}]},
 "radio2":{"up":true,"disabled":false,"config":{"band":"5g","channel":"100","htmode":"HE80"},"interfaces":[{"ifname":"phy2-ap0"}]},
 "radio3":{"up":false,"disabled":true,"config":{"band":"5g"},"interfaces":[]}}
`
	s, err := Parse([]byte(out), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.RadioNames(); !reflect.DeepEqual(got, map[string]string{"wifi0": "2g", "wifi1": "5g", "wifi2": "5g"}) {
		t.Errorf("radios = %v, want one 2.4 GHz and two 5 GHz (radio3 is disabled)", got)
	}
	if got := s.RadioSections(); !reflect.DeepEqual(got, map[string]string{"wifi0": "radio1", "wifi1": "radio2", "wifi2": "radio0"}) {
		t.Errorf("sections = %v", got)
	}
	if r := s.Radios[2]; r.Phy != "" || len(r.VAPs) != 0 || r.Width != 80 || r.Channel != 0 {
		t.Errorf("idle radio = %+v", r)
	}
	rt := s.Fields(nil, CfgInfo{}, nil)["radio_table"].([]map[string]any)
	if len(rt) != 3 || rt[2]["radio"] != "na" {
		t.Errorf("radio_table = %v", rt)
	}
}

// Without the status (older OpenWrt, ubus error) radios stay as iwinfo has them.
func TestRadiosWithoutWirelessStatus(t *testing.T) {
	r := withDevices([]Radio{{Phy: "phy0", Band: "na"}, {Phy: "phy1"}}, nil)
	if r[0].Band != "na" || r[1].Band != "ng" || len(r) != 2 {
		t.Errorf("radios = %+v", r)
	}
}
