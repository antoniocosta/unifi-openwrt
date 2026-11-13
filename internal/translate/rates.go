// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"fmt"
	"sort"
	"strconv"
)

var (
	cckRates  = []int{1000, 2000, 5500, 11000}
	ofdmRates = []int{6000, 9000, 12000, 18000, 24000, 36000, 48000, 54000}
)

// wlanRates maps a WLAN's minimum data rate (wireless.N.minrate_data,
// beacon_rate/mgmt_rate, minrate_cck_rates.status; kbps) to OpenWrt's
// per-iface supported_rates and basic_rate. nil, nil = the band's defaults.
func wlanRates(cfg Cfg, w, band string) (supported, basic []string) {
	min, _ := strconv.Atoi(cfg[w+"minrate_data"])
	cck := band == "2g" && cfg[w+"minrate_cck_rates.status"] != "false"
	all := ofdmRates
	if cck {
		all = append(append([]int{}, cckRates...), ofdmRates...)
	}
	lowest := all[0]
	if band == "2g" && cfg[w+"minrate_cck_rates.status"] == "false" {
		lowest = 6000
	}
	if min <= all[0] && (band != "2g" || cck) {
		return nil, nil // defaults
	}
	if min < lowest {
		min = lowest
	}
	var sup []int
	for _, r := range all {
		if r >= min {
			sup = append(sup, r)
		}
	}
	if len(sup) == 0 {
		return nil, nil
	}
	sort.Ints(sup)
	b, _ := strconv.Atoi(cfg[w+"mgmt_rate"])
	if b < sup[0] {
		b = sup[0]
	}
	basic = []string{strconv.Itoa(sup[0])}
	for _, r := range sup {
		if r == b {
			basic = []string{strconv.Itoa(r)}
		}
	}
	for _, r := range sup {
		supported = append(supported, strconv.Itoa(r))
	}
	return supported, basic
}

type wlanRate struct {
	ssid       string
	sup, basic []string
}

// radioRateOps sets a radio's supported_rates/basic_rate from its WLANs'
// minimum rates: only when every WLAN on the radio raised its minimum (then
// the lowest of them applies, so no WLAN gets stricter than it asked);
// otherwise the radio keeps the defaults. Per-WLAN rates need per-BSS beacon
// rates (nl80211 BEACON_RATE_LEGACY), which e.g. ath10k lacks.
func radioRateOps(rr map[string][]wlanRate, cur UCI, r *Result) []Op {
	var ops []Op
	var radios []string
	for sec := range rr {
		radios = append(radios, sec)
	}
	sort.Strings(radios)
	for _, sec := range radios {
		ws := rr[sec]
		var pick *wlanRate
		raised, dflt := []string{}, []string{}
		for i := range ws {
			if ws[i].sup == nil {
				dflt = append(dflt, ws[i].ssid)
				continue
			}
			raised = append(raised, ws[i].ssid)
			if pick == nil || len(ws[i].sup) > len(pick.sup) { // more rates = lower minimum
				pick = &ws[i]
			}
		}
		var sup, basic []string
		if len(dflt) == 0 && pick != nil {
			sup, basic = pick.sup, pick.basic
		} else if len(raised) > 0 {
			r.Notes = append(r.Notes, fmt.Sprintf("%s: minimum data rate of %v not applied: rates are per radio here and %v use the defaults", sec, raised, dflt))
		}
		k := "wireless." + sec + "."
		for _, x := range []struct {
			opt  string
			want []string
		}{{"supported_rates", sup}, {"basic_rate", basic}} {
			old, has := cur[k+x.opt]
			switch {
			case x.want == nil && has:
				ops = append(ops, Op{Del: true, Key: k + x.opt, Old: old})
			case x.want != nil && !listEq(old, x.want):
				ops = append(ops, Op{Key: k + x.opt, List: x.want, Old: old})
			}
		}
	}
	return ops
}
