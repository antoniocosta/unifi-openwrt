// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"reflect"
	"testing"
)

func TestWLANRates(t *testing.T) {
	c := ParseCfg("w.minrate_data=12000\nw.mgmt_rate=12000\nw.minrate_cck_rates.status=false\nd.minrate_data=1000\nd.minrate_cck_rates.status=true\nn.minrate_data=24000\nn.mgmt_rate=24000\n")
	s, b := wlanRates(c, "w.", "2g")
	if !reflect.DeepEqual(s, []string{"12000", "18000", "24000", "36000", "48000", "54000"}) || !reflect.DeepEqual(b, []string{"12000"}) {
		t.Fatalf("2g 12M: %v %v", s, b)
	}
	if s, b := wlanRates(c, "d.", "2g"); s != nil || b != nil {
		t.Fatalf("2g default: %v %v", s, b)
	}
	if s, b := wlanRates(c, "x.", "5g"); s != nil || b != nil {
		t.Fatalf("5g default: %v %v", s, b)
	}
	s, b = wlanRates(c, "n.", "5g")
	if !reflect.DeepEqual(s, []string{"24000", "36000", "48000", "54000"}) || !reflect.DeepEqual(b, []string{"24000"}) {
		t.Fatalf("5g 24M: %v %v", s, b)
	}
}

func TestRadioRates(t *testing.T) {
	var r Result
	rr := map[string][]wlanRate{"radio0": {
		{"a", []string{"12000", "18000", "24000", "36000", "48000", "54000"}, []string{"12000"}, "5g"},
		{"b", []string{"24000", "36000", "48000", "54000"}, []string{"24000"}, "5g"},
	}}
	ops := radioRateOps(rr, ParseUCI("wireless.radio0=wifi-device\n"), &r)
	if len(ops) != 2 || ops[0].List[0] != "12000" || ops[1].List[0] != "12000" {
		t.Fatalf("all raised -> lowest: %+v", ops)
	}
	rr["radio0"] = append(rr["radio0"], wlanRate{"c", nil, nil, "5g"})
	ops = radioRateOps(rr, ParseUCI("wireless.radio0.supported_rates='12000'\nwireless.radio0.basic_rate='12000'\n"), &r)
	if len(ops) != 2 || !ops[0].Del || len(r.Notes) != 1 {
		t.Fatalf("one default -> defaults: %+v %v", ops, r.Notes)
	}
}

// A 2.4 GHz radio with a WLAN on UniFi's default rates gets the 802.11b
// rates, which OpenWrt leaves out by default.
func TestRadioRatesLegacy(t *testing.T) {
	legacy := func(ops []Op) string {
		for _, o := range ops {
			if o.Key == "wireless.radio0.legacy_rates" {
				return o.Value
			}
		}
		return ""
	}
	var r Result
	dflt := map[string][]wlanRate{"radio0": {{"a", nil, nil, "2g"}}}
	if got := legacy(radioRateOps(dflt, ParseUCI("wireless.radio0=wifi-device\n"), &r)); got != "1" {
		t.Errorf("2.4 GHz on the defaults: legacy_rates op %q, want 1", got)
	}
	if got := legacy(radioRateOps(dflt, ParseUCI("wireless.radio0.legacy_rates='1'\n"), &r)); got != "" {
		t.Errorf("already set: legacy_rates op %q", got)
	}
	five := map[string][]wlanRate{"radio0": {{"a", nil, nil, "5g"}}}
	if got := legacy(radioRateOps(five, ParseUCI("wireless.radio0=wifi-device\n"), &r)); got != "" {
		t.Errorf("5 GHz: legacy_rates op %q", got)
	}
	// every WLAN raised its minimum (no 802.11b rates asked for): the lists rule
	raised := map[string][]wlanRate{"radio0": {{"a", []string{"6000", "9000"}, []string{"6000"}, "2g"}}}
	if got := legacy(radioRateOps(raised, ParseUCI("wireless.radio0=wifi-device\n"), &r)); got != "" {
		t.Errorf("minimum raised: legacy_rates op %q", got)
	}
}
