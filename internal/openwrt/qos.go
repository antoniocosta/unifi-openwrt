// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"strings"
)

// IfLimit is a per-client limit on an AP interface (kbps; 0 = none).
type IfLimit struct {
	Ifname           string
	DownKbps, UpKbps int
}

// qosRuleset: per-client policing with nftables meters on the bridge (each
// client MAC gets its own token bucket): download = frames to the client
// (oifname), upload = frames from it (iifname).
func qosRuleset(ls []IfLimit) (string, error) {
	var sets, rules strings.Builder
	for i, l := range ls {
		if !ifnameOK.MatchString(l.Ifname) {
			return "", fmt.Errorf("bad interface name %q", l.Ifname)
		}
		for _, x := range []struct {
			dir, match, field string
			kbps              int
		}{{"dl", "oifname", "daddr", l.DownKbps}, {"ul", "iifname", "saddr", l.UpKbps}} {
			if x.kbps <= 0 {
				continue
			}
			kbytes := x.kbps / 8
			if kbytes < 1 {
				kbytes = 1
			}
			burst := kbytes / 20 // 50 ms: short transfers stay close to the limit
			if burst < 4 {
				burst = 4
			}
			set := fmt.Sprintf("%s_%d", x.dir, i)
			fmt.Fprintf(&sets, "\tset %s { type ether_addr; size 1024; flags dynamic, timeout; timeout 2m; }\n", set)
			fmt.Fprintf(&rules, "\t\t%s \"%s\" update @%s { ether %s limit rate over %d kbytes/second burst %d kbytes } counter drop\n", x.match, l.Ifname, set, x.field, kbytes, burst)
		}
	}
	return fmt.Sprintf(`table bridge unifi_qos
delete table bridge unifi_qos
table bridge unifi_qos {
%s	chain forward_hook { type filter hook forward priority 10; policy accept;
%s	}
}
`, sets.String(), rules.String()), nil
}

// SetLimits installs (or, with none, removes) the per-client rate limits.
func (cl *Client) SetLimits(ls []IfLimit) error {
	if len(ls) == 0 {
		_, err := cl.Run(`nft list table bridge unifi_qos >/dev/null 2>&1 && nft delete table bridge unifi_qos; true`)
		return err
	}
	rs, err := qosRuleset(ls)
	if err != nil {
		return err
	}
	if out, err := cl.RunStdin(`nft -f -`, []byte(rs)); err != nil {
		return fmt.Errorf("rate limits: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
