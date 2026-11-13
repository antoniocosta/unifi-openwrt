// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// 802.11k neighbour reports (docs/internals.md). A UAP lists its sibling APs
// in the neighbour report it gives clients (with the UniFi's 802.11k on), so
// they find the other AP before the link gets bad. OpenWrt's hostapd only
// knows itself; the daemon adds the siblings its scans found (same SSID,
// matched to a UniFi device on the LAN) with `ubus call hostapd.<if>
// rrm_nr_set`. hostapd forgets the list when it restarts: Sync repeats it.

// Neighbor is one sibling BSS.
type Neighbor struct {
	BSSID, SSID string
	Freq        int
	Channel     int
	Band        string // ng / na
}

// opClass: the global operating class for a 20 MHz channel (IEEE 802.11
// Annex E, table E-4).
func opClass(freq, ch int) int {
	switch {
	case freq < 3000:
		return 81
	case ch >= 36 && ch <= 48:
		return 115
	case ch >= 52 && ch <= 64:
		return 118
	case ch >= 100 && ch <= 144:
		return 121
	case ch >= 149 && ch <= 177:
		return 125
	}
	return 0
}

// NeighborElement: the Neighbor Report element body hostapd wants as hex:
// BSSID, BSSID information (reachable, security, key scope, QoS/APSD/RRM,
// HT, VHT for 5 GHz), operating class, channel, PHY type (HT 7, VHT 9).
func NeighborElement(n Neighbor) (string, error) {
	mac := strings.ReplaceAll(n.BSSID, ":", "")
	b, err := hex.DecodeString(mac)
	if err != nil || len(b) != 6 {
		return "", fmt.Errorf("bad bssid %q", n.BSSID)
	}
	oc := opClass(n.Freq, n.Channel)
	if oc == 0 || n.Channel <= 0 || n.Channel > 255 {
		return "", fmt.Errorf("no operating class for %d MHz ch %d", n.Freq, n.Channel)
	}
	info := uint32(0x3 | 0x4 | 0x8 | 0x20 | 0x80 | 0x800) // reachable, security, key scope, QoS, RRM, HT
	phy := byte(7)
	if n.Freq > 5000 {
		info |= 0x1000 // VHT
		phy = 9
	}
	el := append(b, byte(info), byte(info>>8), byte(info>>16), byte(info>>24), byte(oc), byte(n.Channel), phy)
	return hex.EncodeToString(el), nil
}

// SyncNeighbors sets the neighbour list of an AP interface to its own entry
// plus nbrs (only when it differs). Returns whether it changed.
func (cl *Client) SyncNeighbors(iface string, nbrs []Neighbor) (bool, error) {
	if !ifaceSafe(iface) {
		return false, fmt.Errorf("rrm: bad interface %q", iface)
	}
	out, err := cl.Run("ubus call hostapd." + iface + " rrm_nr_get_own; echo @@; ubus call hostapd." + iface + " rrm_nr_list")
	if err != nil {
		return false, fmt.Errorf("rrm %s: %v", iface, err)
	}
	ownJS, curJS, _ := strings.Cut(string(out), "@@")
	var own struct{ Value []string }
	if json.Unmarshal([]byte(ownJS), &own) != nil || len(own.Value) != 3 {
		return false, fmt.Errorf("rrm %s: no own neighbour entry (802.11k off in hostapd?)", iface)
	}
	want := [][]string{own.Value}
	for _, n := range nbrs {
		el, err := NeighborElement(n)
		if err != nil {
			continue
		}
		want = append(want, []string{strings.ToLower(n.BSSID), n.SSID, el})
	}
	sort.Slice(want[1:], func(i, j int) bool { return want[1+i][0] < want[1+j][0] })
	var cur struct{ List [][]string }
	_ = json.Unmarshal([]byte(curJS), &cur)
	// rrm_nr_list leaves out the own entry
	var curOthers [][]string
	for _, e := range cur.List {
		if len(e) > 0 && !strings.EqualFold(e[0], own.Value[0]) {
			curOthers = append(curOthers, e)
		}
	}
	if sameNR(curOthers, want[1:]) {
		return false, nil
	}
	js, _ := json.Marshal(map[string]any{"list": want})
	if out, err := cl.Run("ubus call hostapd." + iface + " rrm_nr_set " + shQuote(string(js))); err != nil {
		return false, fmt.Errorf("rrm_nr_set %s: %v (%s)", iface, err, firstLine(string(out)))
	}
	return true, nil
}

func sameNR(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(l [][]string) []string {
		var k []string
		for _, e := range l {
			k = append(k, strings.ToLower(strings.Join(e, "|")))
		}
		sort.Strings(k)
		return k
	}
	ka, kb := key(a), key(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// Siblings: BSSes on ssid that belong to another UniFi device on the LAN.
func Siblings(results []ScanResult, ssid string, own map[string]bool, neigh map[string]string) []Neighbor {
	var nb []Neighbor
	seen := map[string]bool{}
	for _, r := range results {
		for _, b := range r.BSS {
			if b.SSID != ssid || own[b.BSSID] || seen[b.BSSID] || parentDevice(b.BSSID, neigh) == "" {
				continue
			}
			seen[b.BSSID] = true
			nb = append(nb, Neighbor{BSSID: b.BSSID, SSID: b.SSID, Freq: b.Freq, Channel: b.Channel, Band: r.Band})
		}
	}
	sort.Slice(nb, func(i, j int) bool { return nb[i].BSSID < nb[j].BSSID })
	return nb
}

// OwnOtherBands: this AP's own BSSes with the same SSID on its other radios
// (a client on 2.4 GHz learns about the 5 GHz BSS of the same AP).
func OwnOtherBands(s *Snapshot, ssid, iface string) []Neighbor {
	var nb []Neighbor
	for _, r := range s.Radios {
		mine := false
		for _, v := range r.VAPs {
			if v.Iface == iface {
				mine = true
			}
		}
		if mine || r.Channel <= 0 {
			continue
		}
		for _, v := range r.VAPs {
			if v.Mode == "Master" && v.SSID == ssid && v.BSSID != "" {
				ref := 2412
				if r.Band == "na" {
					ref = 5180
				}
				nb = append(nb, Neighbor{BSSID: v.BSSID, SSID: ssid, Freq: channelFreq(r.Channel, ref), Channel: r.Channel, Band: r.Band})
			}
		}
	}
	return nb
}
