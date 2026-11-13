// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// planStamgr: the UniFi's per-radio station manager (docs/internals.md),
// sent with the site's advanced features on:
//
//	stamgr.<n>.radio=ng|na|6e
//	stamgr.<n>.minrssi.status=true|false  stamgr.<n>.minrssi.rssi=<dBm>
//	stamgr.<n>.loadbalance.status=true|false  stamgr.<n>.loadbalance.maxsta=<n>
//
// Minimum RSSI -> the radio's rssi_reject_assoc_rssi (hostapd refuses weaker
// clients; the daemon also drops connected ones, cmd/uow). Load balancing ->
// maxassoc on the radio's UniFi-managed interfaces (unifi_* and any
// interface carrying a UniFi SSID).
func planStamgr(cfg Cfg, cur UCI, r *Result) []Op {
	radios := apRadios(cur)
	bands := map[string]string{"ng": "2g", "na": "5g", "6e": "6g"}
	minRSSI := map[string]string{} // radio section -> dBm
	maxSta := map[string]string{}
	for i := 1; i <= 8; i++ {
		p := fmt.Sprintf("stamgr.%d.", i)
		band := cfg[p+"radio"]
		if band == "" {
			continue
		}
		sec := radios[bands[band]]
		if sec == "" {
			r.Notes = append(r.Notes, fmt.Sprintf("stamgr.%d: no %s radio here", i, band))
			continue
		}
		if cfg[p+"minrssi.status"] == "true" {
			if v, err := strconv.Atoi(cfg[p+"minrssi.rssi"]); err == nil && v != 0 {
				if v > 0 { // RSSI above the noise floor -> dBm
					v -= 95
				}
				minRSSI[sec] = strconv.Itoa(v)
			}
		}
		if cfg[p+"loadbalance.status"] == "true" {
			if v, err := strconv.Atoi(cfg[p+"loadbalance.maxsta"]); err == nil && v > 0 {
				maxSta[sec] = strconv.Itoa(v)
			}
		}
	}
	// interfaces the UniFi manages: named unifi_*, or carrying one of its
	// SSIDs (a WLAN adopted onto an existing section, like ap0/ap1 here)
	ssids := map[string]bool{}
	for k, v := range cfg {
		if p := strings.Split(k, "."); len(p) == 3 && p[0] == "wireless" && p[2] == "ssid" && v != "" {
			ssids[v] = true
		}
	}
	var ops []Op
	set := func(key, want string) {
		old, has := cur[key]
		switch {
		case want != "" && old != want:
			ops = append(ops, Op{Key: key, Value: want, Old: old})
		case want == "" && has:
			ops = append(ops, Op{Del: true, Key: key, Old: old})
		}
	}
	var secs []string
	for _, s := range radios {
		secs = append(secs, s)
	}
	sort.Strings(secs)
	for _, sec := range secs {
		set("wireless."+sec+".rssi_reject_assoc_rssi", minRSSI[sec])
		var ifs []string
		for k, v := range cur {
			s := strings.TrimPrefix(k, "wireless.")
			if v == "wifi-iface" && (strings.HasPrefix(s, "unifi_") || ssids[cur[k+".ssid"]]) && cur[k+".device"] == sec && cur[k+".mode"] == "ap" {
				ifs = append(ifs, k)
			}
		}
		sort.Strings(ifs)
		for _, k := range ifs {
			set(k+".maxassoc", maxSta[sec])
		}
	}
	return ops
}

// planOutdoor: radio.outdoor (Indoor/Outdoor in the UniFi). OpenWrt keeps
// the kernel's channel rules per country only; what changes is the
// environment byte of the Country IE clients see (country3 'O' = 0x4f,
// with the Country IE on). Indoor = OpenWrt's default (both unset).
func planOutdoor(cfg Cfg, cur UCI, r *Result) []Op {
	if cfg["radio.outdoor"] == "" {
		return nil
	}
	out := cfg["radio.outdoor"] == "enabled"
	var secs []string
	for k, v := range cur {
		if v == "wifi-device" {
			secs = append(secs, k)
		}
	}
	sort.Strings(secs)
	var ops []Op
	for _, k := range secs {
		for opt, want := range map[string]string{"country3": "0x4f", "ieee80211d": "1"} {
			key := k + "." + opt
			old, has := cur[key]
			switch {
			case out && old != want:
				ops = append(ops, Op{Key: key, Value: want, Old: old})
			case !out && has && old == want && cur[k+".country3"] == "0x4f": // only undo outdoor mode
				ops = append(ops, Op{Del: true, Key: key, Old: old})
			}
		}
	}
	if out && len(ops) > 0 {
		r.Notes = append(r.Notes, "outdoor mode: Country IE environment 'O'; channel limits stay the country's (OpenWrt has no outdoor channel table)")
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Key < ops[j].Key })
	return ops
}
