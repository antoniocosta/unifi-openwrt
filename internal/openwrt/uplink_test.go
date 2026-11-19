// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import "testing"

func TestMeshUplinkFields(t *testing.T) {
	s := &Snapshot{
		MAC:   "08:55:66:77:00:01",
		ARP:   map[string]string{"40:11:22:33:44:01": "192.168.1.1", "02:00:00:00:00:a3": "192.168.1.53"},
		Ports: []Port{{Name: "lan1", Up: false}},
		Radios: []Radio{{Phy: "phy1", Band: "na", Channel: 149, VAPs: []Radio{
			{Iface: "phy1-ap0", Mode: "Master", SSID: "HomeWiFi", BSSID: "c2:00:00:00:00:01",
				Stations: []Station{{MAC: "aa:bb:cc:dd:ee:ff", Signal: -60, Noise: -95}}},
			{Iface: "phy1-sta0", Mode: "Client", SSID: "vwire-x", BSSID: "0e:55:66:77:00:05",
				Stations: []Station{{MAC: "5e:11:22:33:44:04", Signal: -57, Noise: -95, TxRate: 866700, RxRate: 780000}}},
		}}},
	}
	f := s.Fields(nil, CfgInfo{DevName: map[string]string{"vwire-x|wifi0": "vwireap4"}}, nil)
	if f["uplink"] != "ath1" {
		t.Fatalf("uplink = %v, want ath1", f["uplink"])
	}
	if f["num_sta"] != 1 {
		t.Errorf("num_sta = %v: the parent AP is not a client", f["num_sta"])
	}
	var up map[string]any
	for _, v := range f["vap_table"].([]map[string]any) {
		if v["usage"] == "uplink" {
			up = v
		}
	}
	if up == nil || up["ap_mac"] != "5e:11:22:33:44:04" || up["rssi"] != 38 || up["name"] != "ath1" {
		t.Fatalf("uplink vap = %v", up)
	}
	if st := up["sta_table"].([]map[string]any); len(st) != 1 || st[0]["mac"] != "5e:11:22:33:44:04" {
		t.Errorf("uplink sta_table must hold the parent: %v", up["sta_table"])
	}
	if st := up["sta_table"].([]map[string]any); st[0]["serialno"] != "40:11:22:33:44:01" {
		t.Errorf("serialno = %v, want the parent's device MAC", st[0]["serialno"])
	}
	if it, ok := f["if_table"].([]map[string]any); ok && it[0]["up"] != false {
		t.Errorf("eth0 should be down while meshed: %v", it[0])
	}
}

// The parent AP's own MAC is found in the forwarding database when the ARP
// table does not have it (a mesh child has no IP traffic with its parent).
func TestNeighboursFromFDB(t *testing.T) {
	s := &Snapshot{ARP: map[string]string{"aa:bb:cc:00:00:01": "192.168.1.9"},
		FDB: []FDBEntry{{MAC: "40:11:22:33:44:01", Port: 8}, {MAC: "5A:11:22:33:44:03", Port: 8}, {MAC: "02:aa:bb:cc:dd:01", Port: 1, Local: true}}}
	n := s.neighbours()
	if _, ok := n["02:aa:bb:cc:dd:01"]; ok {
		t.Error("the AP's own address counted as a neighbour")
	}
	if n["aa:bb:cc:00:00:01"] != "192.168.1.9" {
		t.Errorf("ARP entry lost: %v", n)
	}
	if got := parentDevice("5a:11:22:33:44:03", n); got != "40:11:22:33:44:01" {
		t.Errorf("parent device = %q, want the AP's own MAC from the FDB", got)
	}
	if got := parentDevice("5a:11:22:33:44:03", s.ARP); got != "" {
		t.Errorf("ARP alone should not find the parent, got %q", got)
	}
}
