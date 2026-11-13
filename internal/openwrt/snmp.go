// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"regexp"
	"strings"
)

// SNMP the UniFi way (docs/internals.md). With SNMP on for the site, the
// controller's system_cfg carries:
//
//	snmp.status=enabled  snmp.version=1_2c|3|all
//	snmp.community=<v1/v2c community>        (1_2c, all)
//	snmp.user.1.name / snmp.user.1.password  (3, all)
//	snmp.location / snmp.contact             (per device, optional)
//
// The daemon writes a net-snmp config (root-only: the community and password
// are secrets) and procd runs snmpd on it (init script instance "snmp").

const SNMPConf = "/etc/unifi-openwrt/snmpd.conf"

var snmpSafe = regexp.MustCompile(`^[\x21-\x7e]{1,64}$`) // printable, no spaces or quotes trouble below

// SNMPConfig builds snmpd.conf from system_cfg keys; "" = SNMP off.
func SNMPConfig(cfg map[string]string) (string, error) {
	if cfg["snmp.status"] != "enabled" {
		return "", nil
	}
	v := cfg["snmp.version"]
	v12 := v == "1_2c" || v == "all" || v == ""
	v3 := v == "3" || v == "all"
	var b strings.Builder
	b.WriteString("# written by unifi-openwrt from the UniFi's snmp settings\nagentaddress udp:161,udp6:161\n")
	text := func(k string) string { // free text: one line, no quotes
		return strings.NewReplacer("\n", " ", "\r", " ", `"`, "'").Replace(cfg[k])
	}
	if s := text("snmp.location"); s != "" {
		fmt.Fprintf(&b, "sysLocation %s\n", s)
	}
	if s := text("snmp.contact"); s != "" {
		fmt.Fprintf(&b, "sysContact %s\n", s)
	}
	n := 0
	if v12 {
		c := cfg["snmp.community"]
		if c == "" {
			c = "public"
		}
		if !snmpSafe.MatchString(c) || strings.ContainsAny(c, `"\`) {
			return "", fmt.Errorf("snmp community with characters snmpd.conf can't hold")
		}
		fmt.Fprintf(&b, "rocommunity %s default\nrocommunity6 %s default\n", c, c)
		n++
	}
	if v3 {
		u, p := cfg["snmp.user.1.name"], cfg["snmp.user.1.password"]
		if u != "" && p != "" {
			if !snmpSafe.MatchString(u) || !snmpSafe.MatchString(p) || strings.ContainsAny(u+p, `"\`) || len(p) < 8 {
				return "", fmt.Errorf("snmp v3 user/password unusable (8+ printable characters, no quotes)")
			}
			fmt.Fprintf(&b, "createUser %s SHA \"%s\" AES \"%s\"\nrouser %s priv\n", u, p, p, u)
			n++
		}
	}
	if n == 0 {
		return "", nil
	}
	return b.String(), nil
}

// SyncSNMP installs or removes the snmpd config; the init script runs snmpd
// while the file exists (a reload starts or stops it).
func (cl *Client) SyncSNMP(conf string) (changed bool, err error) {
	script := `set -e
umask 077
f=` + SNMPConf + `
cat > "$f.new"
if [ ! -s "$f.new" ]; then
	rm -f "$f.new"
	[ -e "$f" ] || { echo same; exit 0; }
	rm -f "$f"; /etc/init.d/unifi-openwrt reload; echo changed; exit 0
fi
if cmp -s "$f.new" "$f"; then rm -f "$f.new"; echo same; exit 0; fi
mv "$f.new" "$f"
[ -x /usr/sbin/snmpd ] || { echo nosnmpd; exit 0; }
/etc/init.d/unifi-openwrt reload
echo changed`
	out, err := cl.RunStdin("sh -c "+shQuote(script), []byte(conf))
	if err != nil {
		return false, fmt.Errorf("snmp: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	switch strings.TrimSpace(string(out)) {
	case "nosnmpd":
		return true, fmt.Errorf("SNMP is on in the UniFi but snmpd is not installed (apk add snmpd)")
	case "changed":
		return true, nil
	}
	return false, nil
}
