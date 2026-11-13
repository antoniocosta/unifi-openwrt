// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"unifi-openwrt/internal/translate"
)

// IfnamesByDevname maps the controller's VAP devnames (wifi0ap5) to the AP's
// interfaces (phy0-ap2), through system_cfg's ssid|radio -> devname.
func (s *Snapshot) IfnamesByDevname(info CfgInfo) map[string]string {
	m := map[string]string{}
	for i, r := range s.Radios {
		radio := fmt.Sprintf("wifi%d", i)
		for _, v := range r.VAPs {
			if d := info.DevName[v.SSID+"|"+radio]; d != "" {
				m[d] = v.Iface
			}
		}
	}
	return m
}

var ifnameOK = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,15}$`)

// guestRuleset renders the nftables bridge table for guest interfaces:
// EAPOL, ARP, DHCP and DNS (to the LAN's DNS servers only) pass; IPv6 and
// the restricted ranges (minus pre-allowed) are dropped; the rest (internet)
// passes. Mirrors the controller's GUESTIN/GUESTOUT ebtables chains, without
// the captive portal.
func guestRuleset(ifnames []string, g translate.Guest, dns []string, brMAC string) (string, error) {
	for _, i := range ifnames {
		if !ifnameOK.MatchString(i) {
			return "", fmt.Errorf("bad interface name %q", i)
		}
	}
	nets := func(vs []string) (string, error) {
		var out []string
		for _, v := range vs {
			if _, err := netip.ParsePrefix(v); err != nil {
				if _, err := netip.ParseAddr(v); err != nil {
					return "", fmt.Errorf("bad address %q", v)
				}
			}
			out = append(out, v)
		}
		return strings.Join(out, ", "), nil
	}
	restricted, err := nets(g.Restricted)
	if err != nil {
		return "", err
	}
	pre, err := nets(g.PreAllow)
	if err != nil {
		return "", err
	}
	dnsl, err := nets(dns)
	if err != nil {
		return "", err
	}
	// guest DNS -> the AP's own forwarder (UniFi GUEST_DNS/REDIRECT_DNS): hand
	// the frame to the AP's IP stack, marked; ip nat redirects it to :5300
	redirect := ""
	if brMAC != "" {
		if _, err := net.ParseMAC(brMAC); err != nil {
			return "", fmt.Errorf("bad bridge MAC %q", brMAC)
		}
		redirect = fmt.Sprintf(`	chain prerouting_hook { type filter hook prerouting priority -300; policy accept;
		iifname @guest_ifs meta l4proto { tcp, udp } th dport 53 meta mark set 0x40000000 meta pkttype set host ether daddr set %s
	}
`, brMAC)
	}
	// captive portal: the portal ports on the console pass; authorized MACs
	// pass; everyone else's web traffic was redirected in prerouting, and
	// the rest is dropped (the controller's GUESTIN policy DROP)
	portalAllow, portalTail := "", ""
	if g.Portal {
		if _, err := netip.ParseAddr(g.PortalHost); err != nil {
			return "", fmt.Errorf("portal host %q is not an IPv4 address", g.PortalHost)
		}
		portalAllow = fmt.Sprintf("\t\tip daddr %s tcp dport { 8880, 8843, 8881, 8882 } accept\n", g.PortalHost)
		portalTail = "\t\tether saddr @authorized accept\n\t\tdrop\n"
		if brMAC != "" {
			redirect = strings.Replace(redirect, "\t}\n", fmt.Sprintf("\t\tiifname @guest_ifs ether saddr != @authorized ip daddr != %s tcp dport 80 meta mark set 0x10000000 meta pkttype set host ether daddr set %s\n\t}\n", g.PortalHost, brMAC), 1)
			// after a portal login the controller sends the browser to
			// <some host>:8882/redirect?ec=...: the AP takes it, whatever the host
			redirect = strings.Replace(redirect, "\t}\n", fmt.Sprintf("\t\tiifname @guest_ifs tcp dport 8882 meta mark set 0x10000000 meta pkttype set host ether daddr set %s\n\t}\n", brMAC), 1)
		}
	}
	q := make([]string, len(ifnames))
	for i, n := range ifnames {
		q[i] = `"` + n + `"`
	}
	elem := func(s string) string {
		if s == "" {
			return ""
		}
		return " elements = { " + s + " };"
	}
	return fmt.Sprintf(`table bridge unifi_guest
delete table bridge unifi_guest
table bridge unifi_guest {
	set guest_ifs { type ifname;%[1]s }
	set restricted { type ipv4_addr; flags interval;%[2]s }
	set pre_allow { type ipv4_addr; flags interval;%[3]s }
	set dns { type ipv4_addr; flags interval;%[4]s }
	set authorized { type ether_addr; flags timeout; }
	chain guest_in {
		meta mark & 0xc0000000 == 0x40000000 accept
		meta mark & 0x30000000 == 0x10000000 accept
		ether type { 0x888e, arp } accept
		udp sport 68 udp dport 67 accept
		ip daddr @dns meta l4proto { tcp, udp } th dport 53 accept
		ether type ip6 drop
		ip daddr @pre_allow accept
%[7]s		ip daddr @restricted drop
%[8]s	}
	chain guest_out {
		ether type ip6 drop
	}
%[5]s	chain forward_hook { type filter hook forward priority 0; policy accept;
		iifname @guest_ifs jump guest_in
		oifname @guest_ifs jump guest_out
	}
	chain input_hook { type filter hook input priority 0; policy accept;
		iifname @guest_ifs tcp dport 8882 accept
		iifname @guest_ifs jump guest_in
	}
}
`, " elements = { "+strings.Join(q, ", ")+" };", elem(restricted), elem(pre), elem(dnsl), redirect, "", portalAllow, portalTail), nil
}

// GuestDNSPort is where the AP's guest DNS forwarder listens. It is not
// port 53, so the AP is no DNS server for the LAN; only redirected guest DNS
// reaches it.
const GuestDNSPort = 5300

const guestDNSStop = `[ -f /var/run/unifi-guest-dns.pid ] && kill $(cat /var/run/unifi-guest-dns.pid) 2>/dev/null; rm -f /var/run/unifi-guest-dns.pid
nft list table ip unifi_guest_nat >/dev/null 2>&1 && nft delete table ip unifi_guest_nat; true`

// SetGuest installs (or, with no interfaces, removes) the guest rules and
// the guest DNS forwarder. Runtime only: call again after an AP reboot.
func (cl *Client) SetGuest(ifnames []string, g translate.Guest) error {
	if len(ifnames) == 0 {
		_, err := cl.Run(`nft list table bridge unifi_guest >/dev/null 2>&1 && nft delete table bridge unifi_guest; ` + guestDNSStop + "\n" + portalStop)
		return err
	}
	// the LAN's DNS servers and gateway as the AP learnt them; the bridge
	// MAC and address (for the DNS redirect)
	out, err := cl.Run(`sed -n 's/^nameserver \([0-9.]*\)$/\1/p' /tmp/resolv.conf.d/resolv.conf.auto 2>/dev/null; ip -4 route show default | sed -n 's/^default via \([0-9.]*\).*/\1/p'; echo "mac $(cat /sys/class/net/br-lan/address)"; echo "ip $(ip -4 addr show br-lan | sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -1)"`)
	if err != nil {
		return err
	}
	var dns []string
	var brMAC, brIP string
	seen := map[string]bool{}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		switch {
		case len(f) == 2 && f[0] == "mac":
			brMAC = f[1]
		case len(f) == 2 && f[0] == "ip":
			brIP = f[1]
		case len(f) == 1:
			if _, err := netip.ParseAddr(f[0]); err == nil && !seen[f[0]] {
				seen[f[0]] = true
				dns = append(dns, f[0])
			}
		}
	}
	// forwarder: DNS only (no dhcp-range = no DHCP), on the AP's address,
	// side port, upstream = the LAN's DNS
	fwd := ""
	portal := portalStop
	if g.Portal {
		portal = portalStart(g.PortalURL)
	}
	if _, err := netip.ParseAddr(brIP); err == nil && brMAC != "" {
		fwd = fmt.Sprintf(`if ! { [ -f /var/run/unifi-guest-dns.pid ] && kill -0 $(cat /var/run/unifi-guest-dns.pid) 2>/dev/null; }; then
  dnsmasq --conf-file=/dev/null --port=%[1]d --listen-address=%[2]s --bind-interfaces --no-hosts --resolv-file=/tmp/resolv.conf.d/resolv.conf.auto --pid-file=/var/run/unifi-guest-dns.pid --user=dnsmasq --group=dnsmasq --cache-size=150 || exit 3
fi
nft -f - <<'NAT'
table ip unifi_guest_nat
delete table ip unifi_guest_nat
table ip unifi_guest_nat {
	chain prerouting { type nat hook prerouting priority dstnat; policy accept;
		meta mark & 0xc0000000 == 0x40000000 meta l4proto { tcp, udp } th dport 53 redirect to :%[1]d
		meta mark & 0x30000000 == 0x10000000 tcp dport 80 redirect to :%[3]d
		meta mark & 0x30000000 == 0x10000000 tcp dport 8882 redirect to :8882
	}
}
NAT
%[4]s`, GuestDNSPort, brIP, PortalPort, portal)
	} else {
		brMAC = "" // no forwarder: guests use the LAN's DNS directly
	}
	rs, err := guestRuleset(ifnames, g, dns, brMAC)
	if err != nil {
		return err
	}
	script := "set -e\n" + fwd + "nft -f /tmp/unifi-guest.nft\nrm -f /tmp/unifi-guest.nft\n"
	if _, err := cl.RunStdin(`cat > /tmp/unifi-guest.nft`, []byte(rs)); err != nil {
		return fmt.Errorf("guest rules: %v", err)
	}
	if out, err := cl.Run("sh -c " + shQuote(script)); err != nil {
		return fmt.Errorf("guest rules: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PortalPort is the AP-side redirector for unauthorized guests' HTTP (a
// separate uhttpd instance; LuCI's stays on 80/443).
const PortalPort = 8085

const portalStop = `[ -f /var/run/unifi-portal.pid ] && kill $(cat /var/run/unifi-portal.pid) 2>/dev/null; rm -f /var/run/unifi-portal.pid; true
`

// portalStart writes the redirect CGI and starts the redirector. The CGI
// answers every request with a 302 to the controller's portal, with the
// parameters a UniFi AP adds: id (client MAC), ap (AP MAC), t, url, ssid.
func portalStart(portalURL string) string {
	cgi := `#!/bin/sh
ip="$REMOTE_ADDR"
mac=$(awk -v ip="$ip" '$1==ip {print $4}' /proc/net/arp | head -1)
ap=$(cat /sys/class/net/br-lan/address)
ssid=""
for o in $(ubus list 'hostapd.*'); do
  ubus call $o get_clients 2>/dev/null | grep -qi "\"$mac\"" && { ssid=$(ubus call $o get_status | jsonfilter -e @.ssid); break; }
done
enc() { printf '%s' "$1" | sed -e 's/%/%25/g' -e 's/ /%20/g' -e 's/&/%26/g' -e 's/?/%3F/g' -e 's/=/%3D/g' -e 's/#/%23/g' -e 's/+/%2B/g' -e 's/"/%22/g'; }
orig="http://$HTTP_HOST$REQUEST_URI"
loc="` + portalURL + `?id=$mac&ap=$ap&t=$(date +%s)&url=$(enc "$orig")&ssid=$(enc "$ssid")"
printf 'Status: 302 Found\r\nLocation: %s\r\nCache-Control: no-cache\r\nContent-Type: text/html\r\n\r\n<a href="%s">Login</a>\n' "$loc" "$loc"
`
	return fmt.Sprintf(`mkdir -p /tmp/unifi-portal/cgi-bin
cat > /tmp/unifi-portal/cgi-bin/r <<'CGI'
%sCGI
chmod 755 /tmp/unifi-portal/cgi-bin/r
if ! { [ -f /var/run/unifi-portal.pid ] && kill -0 $(cat /var/run/unifi-portal.pid) 2>/dev/null; }; then
  uhttpd -f -p 0.0.0.0:%d -h /tmp/unifi-portal -x /cgi-bin -E /cgi-bin/r -D -t 10 -T 5 -A 0 -N 50 &
  echo $! > /var/run/unifi-portal.pid
fi
`, cgi, PortalPort)
}

// AuthorizeGuest lets a guest MAC past the portal for ttl (nft set element
// with a timeout); UnauthorizeGuest removes it.
func (cl *Client) AuthorizeGuest(mac string, ttl time.Duration) error {
	if !macOK.MatchString(mac) {
		return fmt.Errorf("bad MAC %q", mac)
	}
	if ttl < time.Minute {
		ttl = time.Minute
	}
	_, err := cl.Run(fmt.Sprintf(`nft list table bridge unifi_guest >/dev/null 2>&1 || exit 0
nft delete element bridge unifi_guest authorized { %[1]s } 2>/dev/null
nft add element bridge unifi_guest authorized { %[1]s timeout %[2]ds }`, mac, int(ttl.Seconds())))
	return err
}

func (cl *Client) UnauthorizeGuest(mac string) error {
	if !macOK.MatchString(mac) {
		return fmt.Errorf("bad MAC %q", mac)
	}
	_, err := cl.Run(fmt.Sprintf(`nft delete element bridge unifi_guest authorized { %s } 2>/dev/null; true`, mac))
	return err
}
