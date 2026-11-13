// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "sort"

// Steering is the controller's band steering (bandsteering section): mode
// "prefer_5g" | "equal" | "" (off) and the VAP devnames in steering groups.
type Steering struct {
	Mode     string
	Devnames []string
}

func BandSteering(cfg Cfg) Steering {
	if cfg["bandsteering.status"] != "enabled" || cfg["bandsteering.mode"] == "off" {
		return Steering{}
	}
	s := Steering{Mode: cfg["bandsteering.mode"]}
	for _, g := range indices(cfg, "bandsteering") {
		if cfg["bandsteering."+g+".status"] != "enabled" {
			continue
		}
		for _, v := range indices(cfg, "bandsteering."+g+".vap") {
			if d := cfg["bandsteering."+g+".vap."+v+".devname"]; d != "" {
				s.Devnames = append(s.Devnames, d)
			}
		}
	}
	sort.Strings(s.Devnames)
	if len(s.Devnames) == 0 {
		return Steering{}
	}
	return s
}
