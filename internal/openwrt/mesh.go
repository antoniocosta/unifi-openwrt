// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"regexp"
	"strings"
)

// meshScript is the AP-side mesh-uplink watcher (procd instance "mesh"). It
// keeps a 4-address (WDS) client to the site's mesh SSID up only while no
// wired bridge port has link: 20 s without cable -> join; cable back -> the
// wireless uplink goes down within a second, then is removed. So the AP is
// never uplinked by cable and Wi-Fi at once (no loop).
const meshScript = `#!/bin/sh
C=/etc/unifi-openwrt/mesh.conf
STA=unifi_mesh
wired() {
	for p in $(ubus call network.device status | jsonfilter -e '@["br-lan"]["bridge-members"][*]'); do
		case "$p" in phy*|wlan*|mesh*) continue ;; esac
		[ "$(cat /sys/class/net/$p/carrier 2>/dev/null)" = 1 ] && return 0
	done
	return 1
}
meshing() { [ "$(uci -q get wireless.$STA)" = wifi-iface ]; }
drop() {
	for i in $(ubus call network.wireless status | jsonfilter -e "@.*.interfaces[@.section='$STA'].ifname"); do ip link set "$i" down; done
	uci -q delete wireless.$STA; uci commit wireless; wifi reload
	logger -t unifi-mesh "wired uplink back: wireless uplink removed"
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
	logger -t unifi-mesh "no wired uplink for 20 s: joining the mesh as a wireless uplink"
}
meshing && wired && drop
lost=0
while :; do
	# no config (meshing off in the UniFi, or -mesh-uplink off): no wireless uplink
	if [ ! -s "$C" ]; then
		lost=0
		meshing && drop
		sleep 5
		continue
	fi
	MESH_ACCEPT=
	. "$C"   # MESH_SSID MESH_PSK MESH_RADIO MESH_ACCEPT (written by the daemon; re-read each loop)
	if wired; then
		lost=0
		meshing && drop
	else
		lost=$((lost + 1))
		[ $lost -ge 20 ] && ! meshing && join
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
// Mesh Parent BSSID masks (MeshAccept); empty = any parent.
func (cl *Client) SyncMesh(enabled bool, ssid, psk, radio string, accept []string) error {
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
	conf := fmt.Sprintf("MESH_SSID='%s'\nMESH_PSK='%s'\nMESH_RADIO='%s'\nMESH_ACCEPT='%s'\n", ssid, psk, radio, strings.Join(accept, " "))
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
