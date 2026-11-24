// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"regexp"
	"strings"
)

// meshScript is the AP-side mesh-uplink watcher (procd instance "mesh"). It
// keeps a 4-address (WDS) client to the site's mesh SSID up only while no
// cable is the uplink: 20 s without cable -> join; cable back -> the wireless
// uplink goes down within a second, then is removed. So the AP is never
// uplinked by cable and Wi-Fi at once (no loop).
//
// A cable is the uplink when the gateway answers through it, not when it has
// link: a client plugged into the AP, or a cable to a switch that has lost
// its own uplink, has link too. Measured on a Linksys E4200 v2 whose switch
// port was moved to an empty VLAN: with link alone as the rule it stayed off
// the network for as long as that lasted. So:
//   - meshing, and a cable has link: the daemon's own binary sends an ARP
//     request for the gateway out of the cable only ("uow wire-probe",
//     cmd/uow/wireprobe_linux.go), at once when the cable gets link and
//     every 10 s after that. An answer: the cable is the uplink. This does
//     not touch the wireless uplink, so a client plugged into the AP costs
//     nothing.
//   - no answer when the cable gets link, and every 5 minutes: the older
//     test, for a gateway that does not answer such a request and for a
//     daemon that runs elsewhere. The uplink leaves the bridge for up to 2 s
//     and the gateway is pinged; without an answer it goes back in.
//   - on a cable: a gateway that does not answer for 30 s brings the
//     wireless uplink back.
//
// The gateway's MAC in the bridge's forwarding table does not tell where the
// gateway is while both are in the bridge: every broadcast arrives through
// both and the slower copy, the wireless one, is the one that stays. Tried:
// a cable that came alive was noticed after 21 s once and 131 s once.
//
// An AP without a default gateway cannot ask: a cable with link is then the
// uplink, as it always was.
const meshScript = `#!/bin/sh
C=/etc/unifi-openwrt/mesh.conf
STA=unifi_mesh
# ports: the wired bridge ports with link
ports() {
	for p in $(ubus call network.device status | jsonfilter -e '@["br-lan"]["bridge-members"][*]'); do
		case "$p" in phy*|wlan*|mesh*) continue ;; esac
		# any Wi-Fi interface, whatever its name (e.g. wl0.1-ap0 on ath12k MLO builds)
		[ -e /sys/class/net/$p/phy80211 ] && continue
		[ "$(cat /sys/class/net/$p/carrier 2>/dev/null)" = 1 ] && echo "$p"
	done
}
wired() { [ -n "$(ports)" ]; }
meshing() { [ "$(uci -q get wireless.$STA)" = wifi-iface ]; }
gw() { ip route show default 2>/dev/null | while read a b c d; do [ "$b" = via ] && { echo "$c"; break; }; done; }
# the gateway answers within a second (no default gateway: nothing to ask, yes)
reach() { g=$(gw); [ -n "$g" ] || return 0; ping -c 1 -W 1 "$g" >/dev/null 2>&1; }
# wire N: the gateway answers an ARP request sent out of the cables only (N
# tries of 1 s). The wireless uplink is not touched.
wire() {
	g=$(gw); [ -n "$g" ] || return 0
	[ -n "$MESH_UOW" ] && [ -x "$MESH_UOW" ] || return 1
	"$MESH_UOW" wire-probe "$g" "$1" $(ports) >/dev/null 2>&1
}
# probe N: does the gateway answer through a cable? Up to N tries with the
# wireless uplink out of the bridge; it goes back in when there is no answer.
probe() {
	s=$(ubus call network.wireless status | jsonfilter -e "@.*.interfaces[@.section='$STA'].ifname")
	for i in $s; do ip link set "$i" nomaster 2>/dev/null; done
	k=0
	while [ $k -lt $1 ]; do
		reach && return 0
		k=$((k + 1))
	done
	for i in $s; do ip link set "$i" master br-lan 2>/dev/null; done
	return 1
}
linked() {
	for i in $(ubus call network.wireless status | jsonfilter -e "@.*.interfaces[@.section='$STA'].ifname"); do
		case "$(iw dev "$i" link 2>/dev/null)" in Connected*) return 0 ;; esac
	done
	return 1
}
drop() {
	for i in $(ubus call network.wireless status | jsonfilter -e "@.*.interfaces[@.section='$STA'].ifname"); do ip link set "$i" down; done
	uci -q delete wireless.$STA; uci commit wireless; wifi reload
	logger -t unifi-mesh "$1: wireless uplink removed"
}
join() {
	uci batch <<UCI
set wireless.$STA=wifi-iface
set wireless.$STA.device='$MESH_RADIO'
set wireless.$STA.mode='sta'
set wireless.$STA.network='lan'
set wireless.$STA.ssid='$MESH_SSID'
set wireless.$STA.encryption='psk2+ccmp'
set wireless.$STA.key='$MESH_PSK'
set wireless.$STA.wds='1'
UCI
	# Mesh Parent (mesh.serial1/2): accept only the chosen parents' BSSIDs
	for a in $MESH_ACCEPT; do uci add_list wireless.$STA.bssid_whitelist="$a"; done
	uci commit wireless; wifi reload
	logger -t unifi-mesh "$1: joining the mesh as a wireless uplink"
}
MESH_UOW=
[ -s "$C" ] && . "$C"
meshing && wired && { wire 3 || probe 2; } && drop "wired uplink back"
lost=0
down=0
wait=60
dead=0   # looks, on a cable, at a gateway that did not answer
tick=0   # s since the last look (on a cable) or wire test (meshing next to a cable)
slow=0   # s since the last test with the uplink out of the bridge
nolink=1 # no cable had link at the last pass
while :; do
	# no config (meshing off in the UniFi, or -mesh-uplink off): no wireless uplink
	if [ ! -s "$C" ]; then
		lost=0 down=0 wait=60 dead=0
		meshing && drop "meshing off"
		sleep 5
		continue
	fi
	MESH_ACCEPT=
	MESH_UOW=
	. "$C"   # MESH_SSID MESH_PSK MESH_RADIO MESH_ACCEPT MESH_UOW (written by the daemon; re-read each loop)
	if wired && ! meshing; then
		# on a cable: look at the gateway every 5 s
		lost=0 down=0 nolink=0
		tick=$((tick + 1))
		if [ $tick -ge 5 ]; then
			tick=0
			if reach; then
				dead=0 wait=60
			else
				dead=$((dead + 1))
			fi
			if [ $dead -ge 6 ]; then
				dead=0
				join "a cable has link but the gateway has not answered for 30 s"
			fi
		fi
	else
		if wired; then
			# meshing next to a cable with link: is the cable the uplink?
			ok=0
			tick=$((tick + 1))
			slow=$((slow + 1))
			if [ $nolink = 1 ]; then
				tick=0 slow=0
				{ wire 3 || probe 2; } && ok=1
				[ $ok = 0 ] && logger -t unifi-mesh "a cable has link but the gateway does not answer through it: wireless uplink kept"
			elif [ $tick -ge 10 ]; then
				tick=0
				wire 1 && ok=1
			elif [ $slow -ge 300 ]; then
				slow=0
				probe 1 && ok=1
			fi
			nolink=0
			if [ $ok = 1 ]; then
				lost=0 down=0 wait=60 dead=0 tick=0
				drop "wired uplink back"
				sleep 1
				continue
			fi
		else
			nolink=1 tick=0 slow=0 dead=0
			lost=$((lost + 1))
			[ $lost -ge 20 ] && ! meshing && join "no wired uplink for 20 s"
		fi
		# The uplink is configured but does not associate. Seen on mwl8k after a
		# boot with the uplink already in the config: the radio's first setup has
		# the AP waiting for the uplink, the uplink's scans return nothing, and
		# setting the radio up again does not help. Removing the uplink lets the
		# AP start alone; the join above then adds it back to a running radio.
		# With no parent in range this would restart the radio, and its AP,
		# every 80 s for as long as it lasts. So each failed try doubles the
		# wait, up to 10 minutes; an uplink that associates, a cable that is
		# the uplink or meshing switched off start it at 60 s again.
		if meshing && ! linked; then
			down=$((down + 1))
			if [ $down -ge $wait ]; then
				drop "not associated for $wait s"
				down=0
				lost=0
				wait=$((wait * 2))
				[ $wait -gt 600 ] && wait=600
			fi
		else
			down=0
			meshing && wait=60
		fi
	fi
	sleep 1
done
`

var meshSafe = regexp.MustCompile(`^[A-Za-z0-9._@+=:,/~!#%^*()\[\]{}-]{1,63}$`)

// MeshAccept turns the controller's Mesh Parent choice (mesh.serial1/2: the
// parent devices' MACs, any notation) into wpa_supplicant bssid_accept
// entries. A UniFi device's BSSIDs share bytes 2-5 of its MAC (a device
// 40:11:22:33:44:01 -> 5a:11:22:33:44:03/04), so each parent becomes
// "00:11:22:33:44:00/00:ff:ff:ff:ff:00". No parent = any (Auto).
func MeshAccept(serials ...string) []string {
	var out []string
	for _, s := range serials {
		h := strings.ToLower(strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.TrimSpace(s)))
		if len(h) != 12 || strings.Trim(h, "0123456789abcdef") != "" {
			continue
		}
		out = append(out, fmt.Sprintf("00:%s:%s:%s:%s:00/00:ff:ff:ff:ff:00", h[2:4], h[4:6], h[6:8], h[8:10]))
	}
	return out
}

// SyncMesh installs (or removes) the mesh-uplink config and watcher. The PSK
// is a secret: it travels on stdin and is stored root-only. accept: the
// Mesh Parent BSSID masks (MeshAccept); empty = any parent. helper: the
// daemon's binary on the AP, for the watcher's "uow wire-probe"; empty when
// the daemon runs elsewhere.
func (cl *Client) SyncMesh(enabled bool, ssid, psk, radio string, accept []string, helper string) error {
	if !enabled {
		_, err := cl.Run(`rm -f /etc/unifi-openwrt/mesh.conf; [ "$(uci -q get wireless.unifi_mesh)" = wifi-iface ] && { uci delete wireless.unifi_mesh; uci commit wireless; wifi reload; }; true`)
		return err
	}
	for _, v := range []string{ssid, psk} {
		if !meshSafe.MatchString(v) {
			return fmt.Errorf("mesh ssid/psk with characters this AP will not put in a config")
		}
	}
	if !sectionOK.MatchString(radio) {
		return fmt.Errorf("bad radio %q", radio)
	}
	if !meshSafe.MatchString(helper) {
		helper = "" // the watcher then tests a cable the older way
	}
	conf := fmt.Sprintf("MESH_SSID='%s'\nMESH_PSK='%s'\nMESH_RADIO='%s'\nMESH_ACCEPT='%s'\nMESH_UOW='%s'\n", ssid, psk, radio, strings.Join(accept, " "), helper)
	script := `set -e
mkdir -p /etc/unifi-openwrt
umask 077
cat > /etc/unifi-openwrt/mesh.conf.new
mv /etc/unifi-openwrt/mesh.conf.new /etc/unifi-openwrt/mesh.conf
# new file + rename: the running watcher keeps reading its own (old) inode
cat > /etc/unifi-openwrt/mesh.sh.new <<'SCRIPT'
` + meshScript + `SCRIPT
chmod 755 /etc/unifi-openwrt/mesh.sh.new
cmp -s /etc/unifi-openwrt/mesh.sh.new /etc/unifi-openwrt/mesh.sh && rm -f /etc/unifi-openwrt/mesh.sh.new || mv /etc/unifi-openwrt/mesh.sh.new /etc/unifi-openwrt/mesh.sh
# procd starts the "mesh" instance only if mesh.sh exists at service start:
# on the first sync, reload so it starts (unchanged instances keep running)
if [ "$(ubus call service list '{"name":"unifi-openwrt"}' | jsonfilter -e '@[*].instances.mesh.running')" != true ]; then
	/etc/init.d/unifi-openwrt reload
fi
echo ok`
	out, err := cl.RunStdin("sh -c "+shQuote(script), []byte(conf))
	if err != nil {
		return fmt.Errorf("mesh: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
