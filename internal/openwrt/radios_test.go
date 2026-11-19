// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"reflect"
	"strings"
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
	want := map[string]string{"wifi0": "radio1", "wifi1": "radio0", "wifi2": "radio2"}
	if got := s.RadioSections(); !reflect.DeepEqual(got, want) {
		t.Errorf("sections = %v, want %v", got, want)
	}
	if r := s.Radios[1]; r.Phy != "" || len(r.VAPs) != 0 || r.Width != 80 || r.Channel != 0 {
		t.Errorf("idle radio = %+v", r)
	}

	// radio0 comes up (phy0, first in iwinfo order): the names must not move
	up := strings.Replace(out, `"interfaces":[{"section":"default_radio0"}]`, `"interfaces":[{"ifname":"phy0-ap0"}]`, 1)
	up = strings.Replace(up, "=== iface phy1-ap0 phy1", `=== iface phy0-ap0 phy0
{"ssid":"x","bssid":"02:00:00:00:00:00","mode":"Master","channel":36,"frequency":5180}
=== assoc phy0-ap0
{"results":[]}
=== iface phy1-ap0 phy1`, 1)
	s2, err := Parse([]byte(up), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.RadioSections(); !reflect.DeepEqual(got, want) {
		t.Errorf("after radio0 came up: sections = %v, want %v (wifi1/wifi2 swapped)", got, want)
	}
	rt := s.Fields(nil, CfgInfo{}, nil)["radio_table"].([]map[string]any)
	if len(rt) != 3 || rt[2]["radio"] != "na" {
		t.Errorf("radio_table = %v", rt)
	}
}

// One wiphy for three bands (ath12k multi-radio, e.g. TP-Link Archer BE800 on
// ipq95xx): every interface is on phy "wl0", but each wifi-device is its own
// radio. Grouping by phy alone reported one radio once WLANs were up.
func TestRadiosMultiRadioWiphy(t *testing.T) {
	out := `=== board
{"hostname":"ap","model":"m","release":{"description":"OpenWrt"}}
=== iface wl0.0-ap0 wl0
{"ssid":"x","bssid":"02:00:00:00:00:00","mode":"Master","channel":33,"frequency":6115}
=== assoc wl0.0-ap0
{"results":[]}
=== iface wl0.0-ap1 wl0
{"ssid":"e","bssid":"02:00:00:00:00:10","mode":"Master","channel":33,"frequency":6115}
=== assoc wl0.0-ap1
{"results":[]}
=== iface wl0.1-ap0 wl0
{"ssid":"x","bssid":"02:00:00:00:00:01","mode":"Master","channel":36,"frequency":5180}
=== assoc wl0.1-ap0
{"results":[]}
=== iface wl0.2-ap0 wl0
{"ssid":"x","bssid":"02:00:00:00:00:02","mode":"Master","channel":3,"frequency":2422}
=== assoc wl0.2-ap0
{"results":[]}
=== wstatus
{"radio0":{"up":true,"disabled":false,"config":{"band":"6g","channel":"auto","htmode":"EHT40"},"interfaces":[{"ifname":"wl0.0-ap0"},{"ifname":"wl0.0-ap1"}]},
 "radio1":{"up":true,"disabled":false,"config":{"band":"5g","channel":"36","htmode":"EHT40"},"interfaces":[{"ifname":"wl0.1-ap0"}]},
 "radio2":{"up":true,"disabled":false,"config":{"band":"2g","channel":"auto","htmode":"EHT20"},"interfaces":[{"ifname":"wl0.2-ap0"}]}}
`
	s, err := Parse([]byte(out), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Radios) != 3 {
		t.Fatalf("got %d radios, want 3: %+v", len(s.Radios), s.Radios)
	}
	want := map[string]string{"wifi0": "radio2", "wifi1": "radio1", "wifi2": "radio0"}
	if got := s.RadioSections(); !reflect.DeepEqual(got, want) {
		t.Errorf("sections = %v, want %v", got, want)
	}
	for _, r := range s.Radios {
		if r.Section == "radio0" && len(r.VAPs) != 2 {
			t.Errorf("6 GHz radio has %d VAPs, want 2", len(r.VAPs))
		}
	}
}

// Without the status (older OpenWrt, ubus error) radios stay as iwinfo has them.
func TestRadiosWithoutWirelessStatus(t *testing.T) {
	r := withDevices([]Radio{{Phy: "phy0", Band: "na"}, {Phy: "phy1"}}, nil)
	if r[0].Band != "na" || r[1].Band != "ng" || len(r) != 2 {
		t.Errorf("radios = %+v", r)
	}
}

// A radio whose iwinfo htmodes include a 160 MHz mode gets has_ht160, the
// flag the controller needs before it offers 160 MHz.
func TestHT160(t *testing.T) {
	out := `=== board
{"hostname":"ap","model":"m","release":{"description":"OpenWrt"}}
=== iface phy0-ap0 phy0
{"ssid":"x","bssid":"02:00:00:00:00:01","mode":"Master","channel":36,"frequency":5180,"hwmodes":["a","n","ac","ax"],"htmodes":["HT20","VHT80","VHT160","HE80","HE160"]}
=== assoc phy0-ap0
{"results":[]}
=== iface phy1-ap0 phy1
{"ssid":"x","bssid":"02:00:00:00:00:02","mode":"Master","channel":6,"frequency":2437,"hwmodes":["b","g","n","ax"],"htmodes":["HT20","HT40","HE20","HE40"]}
=== assoc phy1-ap0
{"results":[]}
`
	s, err := Parse([]byte(out), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[any]any{}
	for _, r := range s.Fields(nil, CfgInfo{}, nil)["radio_table"].([]map[string]any) {
		got[r["radio"]] = r["has_ht160"]
	}
	if got["na"] != true || got["ng"] != nil {
		t.Errorf("has_ht160 by band = %v, want na only", got)
	}
}
