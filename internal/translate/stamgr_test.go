// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "testing"

func TestStamgr(t *testing.T) {
	cur := UCI{
		"wireless.radio0": "wifi-device", "wireless.radio0.band": "2g",
		"wireless.radio1": "wifi-device", "wireless.radio1.band": "5g",
		"wireless.unifi_radio1_2": "wifi-iface", "wireless.unifi_radio1_2.device": "radio1", "wireless.unifi_radio1_2.mode": "ap",
		"wireless.default_radio1": "wifi-iface", "wireless.default_radio1.device": "radio1", "wireless.default_radio1.mode": "ap",
		// a UniFi WLAN adopted onto an existing section (the OnHub's ap1)
		"wireless.ap1": "wifi-iface", "wireless.ap1.device": "radio1", "wireless.ap1.mode": "ap", "wireless.ap1.ssid": "home",
		"wireless.radio0.rssi_reject_assoc_rssi": "-70",
	}
	cfg := Cfg{"stamgr.2.radio": "na", "stamgr.2.minrssi.status": "true", "stamgr.2.minrssi.rssi": "-75",
		"stamgr.2.loadbalance.status": "true", "stamgr.2.loadbalance.maxsta": "30",
		"wireless.1.ssid": "home",
		"stamgr.1.radio":  "ng", "stamgr.1.minrssi.status": "false", "stamgr.1.minrssi.rssi": "-70"}
	var r Result
	got := map[string]string{}
	for _, o := range planStamgr(cfg, cur, &r) {
		if o.Del {
			got[o.Key] = "<del>"
		} else {
			got[o.Key] = o.Value
		}
	}
	want := map[string]string{
		"wireless.radio1.rssi_reject_assoc_rssi": "-75",
		"wireless.unifi_radio1_2.maxassoc":       "30",
		"wireless.ap1.maxassoc":                  "30",
		"wireless.radio0.rssi_reject_assoc_rssi": "<del>", // switched off in the UniFi
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["wireless.default_radio1.maxassoc"]; ok {
		t.Error("interfaces the UniFi doesn't manage must be left alone")
	}
	// RSSI given above the noise floor
	cfg = Cfg{"stamgr.1.radio": "ng", "stamgr.1.minrssi.status": "true", "stamgr.1.minrssi.rssi": "20"}
	for _, o := range planStamgr(cfg, cur, &r) {
		if o.Key == "wireless.radio0.rssi_reject_assoc_rssi" && o.Value != "-75" {
			t.Errorf("rssi 20 -> %q, want -75", o.Value)
		}
	}
}

func TestOutdoor(t *testing.T) {
	cur := UCI{"wireless.radio0": "wifi-device", "wireless.radio1": "wifi-device"}
	var r Result
	ops := planOutdoor(Cfg{"radio.outdoor": "enabled"}, cur, &r)
	if len(ops) != 4 || ops[0].Key != "wireless.radio0.country3" || ops[0].Value != "0x4f" {
		t.Fatalf("outdoor: %+v", ops)
	}
	cur["wireless.radio0.country3"], cur["wireless.radio0.ieee80211d"] = "0x4f", "1"
	if ops := planOutdoor(Cfg{"radio.outdoor": "disabled"}, cur, &r); len(ops) != 2 || !ops[0].Del {
		t.Fatalf("back indoor: %+v", ops)
	}
	if ops := planOutdoor(Cfg{"radio.outdoor": "disabled"}, UCI{"wireless.radio0": "wifi-device", "wireless.radio0.ieee80211d": "1"}, &r); len(ops) != 0 {
		t.Fatalf("indoor already: %+v", ops)
	}
}

func TestKeyMgmt(t *testing.T) {
	a := "aaa.1."
	cfg := Cfg{a + "wpa.key.1.mgmt": "SAE", a + "wpa3.transition": "enabled"}
	if got := keyMgmt(cfg, a, "sae-mixed", false); got != "SAE WPA-PSK" {
		t.Errorf("transition: %q", got)
	}
	if got := keyMgmt(cfg, a, "sae-mixed", true); got != "SAE FT-SAE WPA-PSK FT-PSK" {
		t.Errorf("transition+ft: %q", got)
	}
	if got := keyMgmt(Cfg{a + "wpa.key.1.mgmt": "WPA-PSK"}, a, "psk2+ccmp", false); got != "WPA-PSK" {
		t.Errorf("wpa2: %q", got)
	}
	if got := keyMgmt(Cfg{}, a, "none", false); got != "" {
		t.Errorf("open: %q", got)
	}
}
