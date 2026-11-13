// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"os"
	"testing"
	"time"
)

func TestParseScan(t *testing.T) {
	b, err := os.ReadFile("testdata/iw-scan.txt")
	if err != nil {
		t.Fatal(err)
	}
	bss := ParseScan(string(b))
	if len(bss) != 6 {
		t.Fatalf("got %d BSS, want 6", len(bss))
	}
	if bss[0].SSID != "" || bss[0].Channel != 6 || bss[0].Security != "wpapsk" || bss[0].Width != 20 || bss[0].AgeMs != 40 {
		t.Errorf("hidden 2.4 GHz: %+v", bss[0])
	}
	if bss[1].SSID != "neighbour2" || bss[1].Channel != 4 {
		t.Errorf("second: %+v", bss[1])
	}
	v := bss[3]
	if v.Freq != 5180 || v.Channel != 36 || v.Width != 80 || v.CenterFreq != 5210 || v.Signal == 0 {
		t.Errorf("5 GHz VHT80: %+v", v)
	}
}

func TestSpectrum(t *testing.T) {
	before := []ChanSurvey{{Freq: 5180, InUse: true, Active: 1000, Busy: 100}}
	after := []ChanSurvey{{Freq: 5180, InUse: true, Active: 1100, Busy: 150, Noise: -101},
		{Freq: 5200, Active: 50, Busy: 10, Noise: -100}, {Freq: 5220, Active: 50, Busy: 0, Noise: -99},
		{Freq: 5240, Active: 50, Busy: 25, Noise: -102}, {Freq: 5260, Active: 0}}
	sp := Spectrum(SurveyDelta(before, after), "na")
	want := map[[2]int]SpectrumEntry{
		{36, 20}: {36, 20, 5180, 50, -101},
		{40, 20}: {40, 20, 5200, 20, -100},
		{36, 40}: {36, 40, 5190, 40, -100},
		{44, 40}: {44, 40, 5230, 25, -99},
		{36, 80}: {36, 80, 5210, 34, -99},
	}
	got := map[[2]int]SpectrumEntry{}
	for _, e := range sp {
		got[[2]int{e.Channel, e.Width}] = e
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%v: got %+v, want %+v", k, got[k], w)
		}
	}
	ng := Spectrum([]ChanSurvey{{Freq: 2412, Active: 32, Busy: 21}, {Freq: 2437, InUse: true, Active: 100, Busy: 30, Noise: -98}}, "ng")
	if len(ng) != 2 || ng[0].Interference != -98 {
		t.Errorf("missing noise -> in-use floor: %+v", ng)
	}
	if _, ok := got[[2]int{52, 20}]; ok {
		t.Error("a channel the scan didn't visit must be left out")
	}
}

func TestScanTable(t *testing.T) {
	now := time.Unix(1000, 0)
	r := ScanResult{Band: "na", Time: now.Add(-5 * time.Second), BSS: []BSS{
		{BSSID: "5e:11:22:33:44:04", SSID: "home", Freq: 5180, Channel: 36, Width: 80, CenterFreq: 5210, Signal: -60, Security: "wpapsk", AgeMs: 2000},
		{BSSID: "aa:bb:cc:dd:ee:01", SSID: "home", Freq: 5180, Signal: -80},
		{BSSID: "aa:bb:cc:dd:ee:02", SSID: "other", Freq: 5200, Channel: 40, Signal: -97},
		{BSSID: "0a:55:66:77:00:02", SSID: "home", Freq: 5180},
	}}
	rows := ScanTable(r, now, map[string]bool{"0a:55:66:77:00:02": true}, map[string]bool{"home": true},
		map[string]string{"40:11:22:33:44:01": "192.168.1.1"})
	if len(rows) != 2 {
		t.Fatalf("rows: %v", rows)
	}
	if rows[0]["is_unifi"] != true || rows[0]["serialno"] != "40:11:22:33:44:01" || rows[0]["age"] != 7 || rows[0]["rssi"] != 35 {
		t.Errorf("UniFi sibling: %v", rows[0])
	}
	if rows[1]["essid"] != "other" || rows[1]["is_unifi"] != false || rows[1]["rssi"] != 0 {
		t.Errorf("neighbour: %v", rows[1])
	}
}

func TestSplitBands(t *testing.T) {
	res := SplitBands([]BSS{{BSSID: "a", Freq: 2412}, {BSSID: "b", Freq: 5745}, {BSSID: "c", Freq: 5180}},
		[]ChanSurvey{{Freq: 2412, Active: 46, Busy: 23, Noise: -105}, {Freq: 5180, Active: 46, Busy: 0, Noise: -100}}, time.Unix(1, 0))
	if len(res["ng"].BSS) != 1 || len(res["na"].BSS) != 2 || len(res["ng"].Spectrum) != 1 || res["ng"].Spectrum[0].Utilization != 50 || len(res["na"].Spectrum) != 1 {
		t.Errorf("split: %+v", res)
	}
}
