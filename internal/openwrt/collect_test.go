// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseOnHub(t *testing.T) {
	b, err := os.ReadFile("testdata/onhub-collect.txt")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(b, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "TP-Link OnHub" || s.IP != "192.168.1.2" || s.Gateway != "192.168.1.1" || s.MAC == "" {
		t.Fatalf("identity: %+v", s)
	}
	if len(s.Radios) != 2 || s.Radios[0].Band != "ng" || s.Radios[1].Band != "na" {
		t.Fatalf("radios: %+v", s.Radios)
	}
	r0, r1 := s.Radios[0], s.Radios[1]
	if r0.Channel != 6 || r0.Width != 20 || r0.NSS != 3 || r0.TxMax != 30 || r1.Channel != 36 || r1.Width != 80 {
		t.Errorf("radio facts: %+v / %+v", r0, r1)
	}
	if len(r0.VAPs) != 1 || len(r0.VAPs[0].Stations) != 1 || r0.VAPs[0].Stations[0].Signal > -10 || r0.VAPs[0].Stations[0].TxRate == 0 {
		t.Errorf("vaps/stations: %+v", r0.VAPs)
	}
	f := s.Fields(s, CfgInfo{IDs: map[string]string{"HomeWiFi": "abc123"}, DevName: map[string]string{"HomeWiFi|wifi1": "ath7"}}, nil)
	vaps := f["vap_table"].([]map[string]any)
	if len(vaps) != 2 || vaps[0]["name"] != "ath0" || vaps[0]["radio_name"] != "wifi0" || vaps[0]["state"] != "RUN" || vaps[0]["id"] != "abc123" || vaps[1]["name"] != "ath7" {
		t.Errorf("vap_table: %v", vaps)
	}
	if st := vaps[0]["sta_table"].([]map[string]any); len(st) != 1 || st[0]["rssi"].(int) <= 0 {
		t.Errorf("sta_table: %v", st)
	}
	if it := f["if_table"].([]map[string]any); len(it) != 1 || it[0]["name"] != "eth0" || it[0]["speed"] != 1000 {
		t.Errorf("if_table: %v", it)
	}
}

func TestWLANIDs(t *testing.T) {
	got := WLANIDs("wireless.1.ssid=HomeWiFi\nwireless.1.id=6aa0\naaa.1.wpa.psk=secret\nwireless.2.ssid=guest\n")
	if got["HomeWiFi"] != "6aa0" || len(got) != 1 {
		t.Fatalf("%v", got)
	}
}

// Two interfaces on one phy must give one radio with two VAPs.
func TestGroupByPhy(t *testing.T) {
	b, _ := os.ReadFile("testdata/onhub-collect.txt")
	s := string(b)
	// duplicate the 2.4 GHz interface section as a second interface on phy1
	i := strings.Index(s, "=== iface phy1-ap0 phy1")
	j := strings.Index(s, "=== iface phy2-ap0 phy2")
	dup := strings.ReplaceAll(s[i:j], "phy1-ap0", "phy1-ap1")
	snap, err := Parse([]byte(s[:j]+dup+s[j:]), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Radios) != 2 || len(snap.Radios[0].VAPs) != 2 {
		t.Fatalf("radios=%d vaps0=%d", len(snap.Radios), len(snap.Radios[0].VAPs))
	}
	f := snap.Fields(nil, CfgInfo{}, nil)
	if n := len(f["radio_table"].([]map[string]any)); n != 2 {
		t.Fatalf("radio_table %d", n)
	}
	if n := len(f["vap_table"].([]map[string]any)); n != 3 {
		t.Fatalf("vap_table %d", n)
	}
}
