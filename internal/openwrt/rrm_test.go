// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"testing"
	"time"
)

func TestNeighborElement(t *testing.T) {
	el, err := NeighborElement(Neighbor{BSSID: "5a:11:22:33:44:04", Freq: 5745, Channel: 149})
	if err != nil {
		t.Fatal(err)
	}
	// bssid, info 0x000018af little-endian, class 125, channel 149, VHT
	if want := "5a1122334404af1800007d9509"; el != want {
		t.Errorf("got %s, want %s", el, want)
	}
	el, _ = NeighborElement(Neighbor{BSSID: "42:11:22:33:44:02", Freq: 2412, Channel: 1})
	if want := "421122334402af0800005101" + "07"; el != want {
		t.Errorf("2.4 GHz: got %s, want %s", el, want)
	}
	if _, err := NeighborElement(Neighbor{BSSID: "x", Freq: 5745, Channel: 149}); err == nil {
		t.Error("bad bssid accepted")
	}
}

func TestSiblings(t *testing.T) {
	res := []ScanResult{{Band: "na", BSS: []BSS{
		{BSSID: "5a:11:22:33:44:04", SSID: "home", Freq: 5745, Channel: 149},
		{BSSID: "aa:bb:cc:dd:ee:ff", SSID: "home", Freq: 5180, Channel: 36}, // not a LAN device
		{BSSID: "5a:11:22:33:44:05", SSID: "other", Freq: 5745, Channel: 149},
	}}}
	nb := Siblings(res, "home", nil, map[string]string{"40:11:22:33:44:01": "192.168.1.1"})
	if len(nb) != 1 || nb[0].BSSID != "5a:11:22:33:44:04" {
		t.Errorf("siblings: %+v", nb)
	}
}

func TestSatisfaction(t *testing.T) {
	// sub-scores, as in UAP firmware 6.8.2
	for _, c := range []struct{ kbps, avail, want int }{
		{866700, 100, 100}, {866700, 15, 99}, {100000, 100, 98}, {12000, 100, 84}, {6000, 100, 71}, {500, 100, 0},
	} {
		if got := phyScore(c.kbps, c.avail, true); got != c.want {
			t.Errorf("phy %d kbps avail %d: %d, want %d", c.kbps, c.avail, got, c.want)
		}
	}
	if phyScore(6000, 100, false) != 100 {
		t.Error("2.4 GHz clients always score 100 on PHY rate")
	}
	for _, c := range []struct {
		r, a int64
		want int
	}{{5, 20, 100}, {10, 100, 100}, {50, 100, 88}, {67, 100, 79}, {100, 100, 62}} {
		if got := retryScore(c.r, c.a); got != c.want {
			t.Errorf("retries %d/%d: %d, want %d", c.r, c.a, got, c.want)
		}
	}
	// a slow 5 GHz client: real score below 60, reported 60, anomaly bit 2
	satStates = map[string]*satState{}
	t0 := time.Unix(1000, 0)
	st := Station{MAC: "aa", TxRate: 500}
	r := staSatisfaction(st, true, 100, t0)
	if r.Real != 0 || r.Reported != 60 || r.Anomalies != anomalyPHYRate {
		t.Errorf("slow client: %+v", r)
	}
	// within the cycle the result stands; later cycles average
	st.TxRate = 866700
	if r2 := staSatisfaction(st, true, 100, t0.Add(time.Second)); r2 != r {
		t.Errorf("same cycle: %+v", r2)
	}
	if r3 := staSatisfaction(st, true, 100, t0.Add(6*time.Second)); r3.Real != 50 || r3.Now != 100 || r3.Reported != 60 {
		t.Errorf("second cycle: %+v", r3)
	}
	// heavy retries in one cycle
	st2 := Station{MAC: "bb", TxRate: 866700}
	staSatisfaction(st2, true, 100, t0)
	st2.TxPackets, st2.TxRetries = 100, 100 // 100 retries of 200 attempts = 50 %
	if r := staSatisfaction(st2, true, 100, t0.Add(6*time.Second)); r.Now != 88 || r.Real != 94 {
		t.Errorf("retries: %+v", r)
	}
}

func TestKeepSeenAndOwnBands(t *testing.T) {
	t0 := time.Unix(10000, 0)
	old := ScanResult{Time: t0.Add(-20 * time.Minute), BSS: []BSS{{BSSID: "a"}, {BSSID: "b"}}}
	stale := ScanResult{Time: t0.Add(-2 * time.Hour), BSS: []BSS{{BSSID: "z"}}}
	cur := KeepSeen(old, ScanResult{Time: t0, BSS: []BSS{{BSSID: "a"}}}, time.Hour)
	if len(cur.BSS) != 2 || cur.BSS[1].BSSID != "b" || !cur.BSS[1].Seen.Equal(old.Time) || !cur.BSS[0].Seen.Equal(t0) {
		t.Errorf("keep: %+v", cur.BSS)
	}
	if c := KeepSeen(stale, ScanResult{Time: t0}, time.Hour); len(c.BSS) != 0 {
		t.Errorf("stale kept: %+v", c.BSS)
	}
	rows := ScanTable(cur, t0, nil, nil, nil)
	if len(rows) != 0 { // no freq in these fixtures
		t.Errorf("rows: %v", rows)
	}
	s := &Snapshot{Radios: []Radio{
		{Band: "ng", Channel: 6, VAPs: []Radio{{Iface: "phy0-ap0", Mode: "Master", SSID: "home", BSSID: "08:55:66:77:00:03"}}},
		{Band: "na", Channel: 36, VAPs: []Radio{{Iface: "phy1-ap0", Mode: "Master", SSID: "home", BSSID: "08:55:66:77:00:05"}, {Iface: "phy1-ap1", Mode: "Master", SSID: "x", BSSID: "0a:55:66:77:00:05"}}},
	}}
	nb := OwnOtherBands(s, "home", "phy0-ap0")
	if len(nb) != 1 || nb[0].BSSID != "08:55:66:77:00:05" || nb[0].Freq != 5180 || nb[0].Channel != 36 {
		t.Errorf("own other bands: %+v", nb)
	}
}

func TestPortTable(t *testing.T) {
	s := &Snapshot{MAC: "08:55:66:77:00:01", Ports: []Port{
		{Name: "lan1", MAC: "08:55:66:77:00:01", Up: true, Speed: 1000, FullDuplex: true, RxBytes: 5},
		{Name: "teql0"},
		{Name: "wan", MAC: "08:55:66:77:00:04", Speed: -1},
	}}
	m := s.Fields(nil, CfgInfo{}, nil)
	rows, _ := m["port_table"].([]map[string]any)
	if len(rows) != 2 || rows[0]["name"] != "Main" || rows[0]["is_uplink"] != true || rows[0]["speed"] != 1000 ||
		rows[1]["name"] != "Secondary" || rows[1]["up"] != false || rows[1]["speed"] != 0 || m["has_eth1"] != true {
		t.Errorf("port_table: %v has_eth1 %v", rows, m["has_eth1"])
	}
	if it := m["if_table"].([]map[string]any); it[0]["num_port"] != 2 {
		t.Errorf("if_table: %v", it)
	}
}

func TestMeshAccept(t *testing.T) {
	got := MeshAccept("40:11:22:33:44:01", "", "401122334402", "junk")
	want := []string{"00:11:22:33:44:00/00:ff:ff:ff:ff:00", "00:11:22:33:44:00/00:ff:ff:ff:ff:00"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v", got)
	}
	if MeshAccept("", "") != nil {
		t.Fatal("auto should be empty")
	}
}
