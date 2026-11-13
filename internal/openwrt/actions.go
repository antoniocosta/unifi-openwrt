// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// SetLED shows a named state on the status LEDs (tools): on (blue), off,
// locate, white, starting, busy, or a colour "#rrggbb". The daemon uses
// ShowLED with the UniFi state (cmd/uow ledWanted).
func (cl *Client) SetLED(mode string) error {
	s := LEDBlue
	switch mode {
	case "on", "blue":
	case "off":
		s = LEDDark
	case "white":
		s = LEDWhite
	case "locate":
		s.Blink = FastFlash
	case "starting":
		s, s.Blink = LEDWhite, Flash
	case "busy":
		s.Blink = Alternate
	default:
		var r, g, b uint8
		if n, _ := fmt.Sscanf(mode, "#%02x%02x%02x", &r, &g, &b); n != 3 {
			return fmt.Errorf("unknown LED mode %q", mode)
		}
		s.R, s.G, s.B = r, g, b
	}
	return cl.ShowLED(s)
}

// Reboot restarts the AP (UniFi "Restart"). It returns before the AP goes
// down.
func (cl *Client) Reboot() error {
	_, err := cl.Run(`logger -t unifi-openwrt "restart requested by the UniFi controller"; (sleep 3; reboot) >/dev/null 2>&1 </dev/null &`)
	cl.Close()
	return err
}

// BanTime is how long one EnforceBlocked call keeps a blocked client out.
// hostapd has no "unban", so bans are short and refreshed by the daemon: an
// unblock takes effect within BanTime.
const BanTime = 90 * time.Second

// hostapdLoop runs body for every hostapd ubus object ($o) and each MAC ($m).
func hostapdLoop(macs []string, body string) (string, error) {
	for _, m := range macs {
		if !macOK.MatchString(m) {
			return "", fmt.Errorf("bad MAC %q", m)
		}
	}
	return fmt.Sprintf(`n=0; for o in $(ubus list 'hostapd.*'); do for m in %s; do %s; n=$((n+1)); done; done; echo "calls=$n"`,
		strings.Join(macs, " "), body), nil
}

var macOK = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)

// Kick disconnects a client from every BSS (UniFi "Reconnect"); it may come
// straight back.
func (cl *Client) Kick(mac string) error {
	s, err := hostapdLoop([]string{mac}, `ubus call $o del_client "{\"addr\":\"$m\",\"reason\":5,\"deauth\":true,\"ban_time\":0}" >/dev/null 2>&1`)
	if err != nil {
		return err
	}
	_, err = cl.Run(s)
	return err
}

// EnforceBlocked keeps the given clients off every BSS. With hostapd_cli it
// puts them on hostapd's runtime deny list (hostapd disconnects them; lost on
// a Wi-Fi reload, so call it again after one); without it, it falls back to a
// BanTime ban. Idempotent: call it periodically.
func (cl *Client) EnforceBlocked(macs []string) error {
	if len(macs) == 0 {
		return nil
	}
	s, err := hostapdLoop(macs, fmt.Sprintf(`i=${o#hostapd.}; if command -v hostapd_cli >/dev/null && [ -S /var/run/hostapd/$i ]; then hostapd_cli -i $i deny_acl SHOW | grep -qi "^$m " || hostapd_cli -i $i deny_acl ADD_MAC $m >/dev/null; else ubus call $o del_client "{\"addr\":\"$m\",\"reason\":5,\"deauth\":true,\"ban_time\":%d}" >/dev/null 2>&1; fi`, BanTime.Milliseconds()))
	if err != nil {
		return err
	}
	_, err = cl.Run(s)
	return err
}

// Unblock takes clients off the runtime deny list right away, except where
// the BSS's own MAC filter (UniFi MAC filter, maclist file) denies them.
// Timed bans (fallback mode) cannot be lifted and run out within BanTime.
func (cl *Client) Unblock(macs []string) error {
	if len(macs) == 0 {
		return nil
	}
	s, err := hostapdLoop(macs, `i=${o#hostapd.}; command -v hostapd_cli >/dev/null && [ -S /var/run/hostapd/$i ] && ! grep -qi "^$m" /var/run/hostapd-$i.maclist 2>/dev/null && hostapd_cli -i $i deny_acl DEL_MAC $m >/dev/null`)
	if err != nil {
		return err
	}
	_, err = cl.Run(s)
	return err
}

// Bans lists, per BSS, the timed bans and the runtime deny list.
func (cl *Client) Bans() (string, error) {
	out, err := cl.Run(`for o in $(ubus list 'hostapd.*'); do i=${o#hostapd.}; echo "$o bans: $(ubus call $o list_bans | jsonfilter -e '@.clients[*]' 2>/dev/null | tr '\n' ' ') deny: $(hostapd_cli -i $i deny_acl SHOW 2>/dev/null | cut -d' ' -f1 | tr '\n' ' ')"; done`)
	return string(out), err
}
