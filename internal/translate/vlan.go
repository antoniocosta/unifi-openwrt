// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// VLANs (docs/internals.md). For a WLAN on a tagged network the UniFi sends a
// second bridge named after the VLAN, holding the WLAN's VAPs and the tagged
// uplink, with no address for the AP:
//
//	vlan.1.devname=eth0  vlan.1.id=77
//	bridge.2.devname=br0.77
//	bridge.2.port.1.devname=wifi0ap5   (the VAP)
//	bridge.2.port.3.devname=eth0.77    (the cable, tagged)
//	netconf.8.devname=br0.77  netconf.8.ip=0.0.0.0
//
// OpenWrt: a bridge br-uow<vid> over <uplink port>.<vid> (an 802.1Q device on
// the cable port; br-lan stays as it is), an interface uow<vid> with proto
// none (the AP has no address there: no DHCP, DNS or RA from it), and the
// WLAN's wifi-ifaces get network=uow<vid>.

var vlanBridge = regexp.MustCompile(`\.(\d{1,4})$`)

// vlanNetworks: VAP devname -> OpenWrt network name, and the VLAN ids in use.
func vlanNetworks(cfg Cfg) (map[string]string, []string) {
	vaps := map[string]bool{}
	for _, n := range indices(cfg, "wireless") {
		if d := cfg["wireless."+n+".devname"]; d != "" {
			vaps[d] = true
		}
	}
	netOf := map[string]string{}
	seen := map[string]bool{}
	for _, n := range indices(cfg, "bridge") {
		m := vlanBridge.FindStringSubmatch(cfg["bridge."+n+".devname"])
		if m == nil || m[1] == "0" {
			continue
		}
		for k, v := range cfg {
			if strings.HasPrefix(k, "bridge."+n+".port.") && strings.HasSuffix(k, ".devname") && vaps[v] {
				netOf[v] = "uow" + m[1]
				seen[m[1]] = true
			}
		}
	}
	var ids []string
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return netOf, ids
}

// planVLAN creates or removes the per-VLAN bridge and interface.
func planVLAN(cfg Cfg, cur UCI, r *Result) []Op {
	_, ids := vlanNetworks(cfg)
	// the cable port: the first port of br-lan
	port := ""
	for k, v := range cur {
		if strings.HasPrefix(k, "network.") && strings.HasSuffix(k, ".name") && v == "br-lan" {
			f := strings.Fields(strings.ReplaceAll(cur[strings.TrimSuffix(k, ".name")+".ports"], "'", ""))
			if len(f) > 0 {
				port = f[0]
			}
		}
	}
	var ops []Op
	want := map[string]bool{}
	for _, id := range ids {
		if port == "" {
			r.Notes = append(r.Notes, fmt.Sprintf("vlan %s: no br-lan port found in the network config: not set up", id))
			continue
		}
		dev, ifc := "network.uowvlan"+id, "network.uow"+id
		want[dev], want[ifc] = true, true
		if cur[dev] != "device" {
			ops = append(ops, Op{Key: dev, Value: "device"})
			r.Notes = append(r.Notes, fmt.Sprintf("vlan %s: bridge br-uow%s over %s.%s, no address on the AP", id, id, port, id))
		}
		for _, kv := range [][2]string{{"name", "br-uow" + id}, {"type", "bridge"}} {
			if cur[dev+"."+kv[0]] != kv[1] {
				ops = append(ops, Op{Key: dev + "." + kv[0], Value: kv[1], Old: cur[dev+"."+kv[0]]})
			}
		}
		if p := port + "." + id; strings.ReplaceAll(cur[dev+".ports"], "'", "") != p {
			ops = append(ops, Op{Key: dev + ".ports", List: []string{p}, Old: cur[dev+".ports"]})
		}
		if cur[ifc] != "interface" {
			ops = append(ops, Op{Key: ifc, Value: "interface"})
		}
		for _, kv := range [][2]string{{"device", "br-uow" + id}, {"proto", "none"}} {
			if cur[ifc+"."+kv[0]] != kv[1] {
				ops = append(ops, Op{Key: ifc + "." + kv[0], Value: kv[1], Old: cur[ifc+"."+kv[0]]})
			}
		}
	}
	// remove what we created for VLANs that are gone
	var stale []string
	for k, v := range cur {
		s := strings.TrimPrefix(k, "network.")
		if strings.Contains(s, ".") || want[k] {
			continue
		}
		if (v == "device" && regexp.MustCompile(`^uowvlan\d+$`).MatchString(s)) || (v == "interface" && regexp.MustCompile(`^uow\d+$`).MatchString(s)) {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		ops = append(ops, Op{Del: true, Key: k, Old: cur[k]})
	}
	return ops
}
