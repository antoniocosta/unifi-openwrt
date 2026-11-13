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
		{"a", []string{"12000", "18000", "24000", "36000", "48000", "54000"}, []string{"12000"}},
		{"b", []string{"24000", "36000", "48000", "54000"}, []string{"24000"}},
	}}
	ops := radioRateOps(rr, ParseUCI("wireless.radio0=wifi-device\n"), &r)
	if len(ops) != 2 || ops[0].List[0] != "12000" || ops[1].List[0] != "12000" {
		t.Fatalf("all raised -> lowest: %+v", ops)
	}
	rr["radio0"] = append(rr["radio0"], wlanRate{"c", nil, nil})
	ops = radioRateOps(rr, ParseUCI("wireless.radio0.supported_rates='12000'\nwireless.radio0.basic_rate='12000'\n"), &r)
	if len(ops) != 2 || !ops[0].Del || len(r.Notes) != 1 {
		t.Fatalf("one default -> defaults: %+v %v", ops, r.Notes)
	}
}
