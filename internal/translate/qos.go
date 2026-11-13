// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "strconv"

// VAPLimit is a per-client rate limit on one VAP (the controller's user
// group limits, system_cfg qos.vap.N ... maxspeed, kbps; 0 = none).
type VAPLimit struct {
	Devname  string
	DownKbps int
	UpKbps   int
}

// QoSLimits reads the VAPs with a maximum speed.
func QoSLimits(cfg Cfg) []VAPLimit {
	if cfg["qos.status"] != "enabled" {
		return nil
	}
	var out []VAPLimit
	for _, n := range indices(cfg, "qos.vap") {
		p := "qos.vap." + n + "."
		down, _ := strconv.Atoi(cfg[p+"dwnlink.maxspeed"])
		up := 0
		for _, u := range indices(cfg, "qos.vap."+n+".uplink") {
			if v, _ := strconv.Atoi(cfg[p+"uplink."+u+".maxspeed"]); v > 0 && (up == 0 || v < up) {
				up = v
			}
		}
		if (down > 0 || up > 0) && cfg[p+"devname"] != "" {
			out = append(out, VAPLimit{Devname: cfg[p+"devname"], DownKbps: down, UpKbps: up})
		}
	}
	return out
}
