// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "fmt"

// planNetwork maps the management interface's IP settings (netconf on the
// bridge, dhcpc, route, resolv) to OpenWrt's network.lan. apAddr is the
// address the daemon reaches the AP on: a static address that differs from it
// is refused (the daemon would lose the AP).
func planNetwork(cfg Cfg, cur UCI, apAddr string, r *Result) []Op {
	if cur["network.lan"] != "interface" || cfg["netconf.status"] != "enabled" {
		return nil
	}
	bridge := cfg["connectivity.uplink_bridge"]
	if bridge == "" {
		bridge = "br0"
	}
	var nc string
	for _, n := range indices(cfg, "netconf") {
		if cfg["netconf."+n+".devname"] == bridge {
			nc = "netconf." + n + "."
		}
	}
	if nc == "" {
		return nil
	}
	dhcp := false
	for _, n := range indices(cfg, "dhcpc") {
		if cfg["dhcpc."+n+".devname"] == bridge && cfg["dhcpc."+n+".status"] == "enabled" {
			dhcp = true
		}
	}
	k := "network.lan."
	var ops []Op
	if dhcp {
		if cur[k+"proto"] != "dhcp" {
			ops = append(ops, Op{Key: k + "proto", Value: "dhcp", Old: cur[k+"proto"]})
		}
		for _, o := range []string{"ipaddr", "netmask", "gateway", "dns"} {
			if old, ok := cur[k+o]; ok {
				ops = append(ops, Op{Del: true, Key: k + o, Old: old})
			}
		}
		return ops
	}
	ip, mask := cfg[nc+"ip"], cfg[nc+"netmask"]
	if ip == "" || ip == "0.0.0.0" || mask == "" {
		r.Notes = append(r.Notes, "netconf: static without an address: IP settings left alone")
		return nil
	}
	if apAddr != "" && ip != apAddr {
		r.Notes = append(r.Notes, fmt.Sprintf("netconf: static %s differs from the address the AP is reached on (%s): not applied (would lose the AP)", ip, apAddr))
		return nil
	}
	var gw string
	for _, n := range indices(cfg, "route") {
		p := "route." + n + "."
		if cfg[p+"status"] == "enabled" && cfg[p+"ip"] == "0.0.0.0" && cfg[p+"gateway"] != "" {
			gw = cfg[p+"gateway"]
		}
	}
	var dns []string
	for _, n := range indices(cfg, "resolv.nameserver") {
		p := "resolv.nameserver." + n + "."
		if cfg[p+"status"] != "disabled" && cfg[p+"ip"] != "" {
			dns = append(dns, cfg[p+"ip"])
		}
	}
	want := map[string]string{"proto": "static", "ipaddr": ip, "netmask": mask}
	if gw != "" {
		want["gateway"] = gw
	}
	for _, o := range []string{"proto", "ipaddr", "netmask", "gateway"} {
		if v, ok := want[o]; ok && cur[k+o] != v {
			ops = append(ops, Op{Key: k + o, Value: v, Old: cur[k+o]})
		} else if !ok {
			if old, has := cur[k+o]; has {
				ops = append(ops, Op{Del: true, Key: k + o, Old: old})
			}
		}
	}
	if len(dns) > 0 && !listEq(cur[k+"dns"], dns) {
		ops = append(ops, Op{Key: k + "dns", List: dns, Old: cur[k+"dns"]})
	} else if len(dns) == 0 {
		if old, has := cur[k+"dns"]; has {
			ops = append(ops, Op{Del: true, Key: k + "dns", Old: old})
		}
	}
	return ops
}
