// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import (
	"net/url"
	"sort"
)

// Guest is what the controller wants for guest WLANs: the VAP devnames
// (aaa.N.is_guest) and the address ranges guests may not reach (ipset
// guest_restricted) except PreAllow (ipset guest_pre_allow).
type Guest struct {
	Devnames   []string
	Restricted []string
	PreAllow   []string
	// captive portal (redirector section): unauthorized guests' web traffic
	// goes to PortalURL; only DHCP, DNS and PortalHost's portal ports pass.
	Portal     bool
	PortalURL  string
	PortalHost string
}

// GuestPolicy reads the guest VAPs and ipsets from system_cfg.
func GuestPolicy(cfg Cfg) Guest {
	var g Guest
	seen := map[string]bool{}
	for _, n := range indices(cfg, "aaa") {
		p := "aaa." + n + "."
		if cfg[p+"is_guest"] == "true" && cfg[p+"status"] != "disabled" && cfg[p+"devname"] != "" && !seen[cfg[p+"devname"]] {
			seen[cfg[p+"devname"]] = true
			g.Devnames = append(g.Devnames, cfg[p+"devname"])
		}
	}
	sort.Strings(g.Devnames)
	for _, n := range indices(cfg, "ipset") {
		p := "ipset." + n + "."
		var vals []string
		for _, e := range indices(cfg, "ipset."+n+".entry") {
			if v := cfg[p+"entry."+e+".value"]; v != "" {
				vals = append(vals, v)
			}
		}
		switch cfg[p+"name"] {
		case "guest_restricted":
			g.Restricted = vals
		case "guest_pre_allow":
			g.PreAllow = vals
		}
	}
	if cfg["redirector.status"] == "enabled" && cfg["redirector.url"] != "" {
		if u, err := url.Parse(cfg["redirector.url"]); err == nil && u.Hostname() != "" {
			g.Portal, g.PortalURL, g.PortalHost = true, cfg["redirector.url"], u.Hostname()
		}
	}
	if len(g.Devnames) > 0 && len(g.Restricted) == 0 {
		// the controller's default (RFC1918 + multicast)
		g.Restricted = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "224.0.0.0/4"}
	}
	return g
}
