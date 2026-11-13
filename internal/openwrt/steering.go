// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"strings"
)

// SetBandSteering configures usteer (local mode: this AP only) for the
// controller's band steering, or stops it. ssids: the steered SSIDs.
func (cl *Client) SetBandSteering(mode string, ssids []string) error {
	if mode == "" || len(ssids) == 0 {
		_, err := cl.Run(`[ -x /etc/init.d/usteer ] || exit 0; /etc/init.d/usteer stop; /etc/init.d/usteer disable; true`)
		return err
	}
	var b strings.Builder
	b.WriteString("set usteer.@usteer[0].local_mode='1'\nset usteer.@usteer[0].syslog='1'\ndelete usteer.@usteer[0].ssid_list\n")
	for _, s := range ssids {
		if strings.ContainsAny(s, "'\n\r") {
			return fmt.Errorf("bad ssid %q", s)
		}
		fmt.Fprintf(&b, "add_list usteer.@usteer[0].ssid_list='%s'\n", s)
	}
	switch mode {
	case "prefer_5g":
		b.WriteString("set usteer.@usteer[0].band_steering_interval='120000'\nset usteer.@usteer[0].band_steering_min_snr='-60'\nset usteer.@usteer[0].load_balancing_threshold='0'\n")
	case "equal":
		b.WriteString("set usteer.@usteer[0].band_steering_interval='0'\nset usteer.@usteer[0].load_balancing_threshold='5'\n")
	default:
		return fmt.Errorf("unknown band steering mode %q", mode)
	}
	out, err := cl.RunStdin(`command -v usteerd >/dev/null || { echo "usteer not installed"; exit 3; }
uci batch && uci commit usteer && /etc/init.d/usteer enable && /etc/init.d/usteer restart && echo ok`, []byte(b.String()))
	if err != nil {
		return fmt.Errorf("band steering: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
