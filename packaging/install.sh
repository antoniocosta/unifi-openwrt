#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# unifi-openwrt installer: run on the OpenWrt AP, from a folder that holds
# this script, bin/uow-<arch> and files/.  sh install.sh [--no-packages]
set -e
cd "$(dirname "$0")"

say() { echo "unifi-openwrt: $*"; }
die() { say "$*"; exit 1; }

[ -f /etc/openwrt_release ] || die "this is not OpenWrt"
case "$(uname -m)" in
	armv7*) arch=armv7 ;;
	aarch64) arch=aarch64 ;;
	mips) arch=mips ;;
	mipsel|mipsle) arch=mipsel ;;
	x86_64) arch=x86_64 ;;
	*) die "unsupported CPU $(uname -m)" ;;
esac
[ -x "bin/uow-$arch" ] || [ -f "bin/uow-$arch" ] || die "missing bin/uow-$arch"
need=$(( $(wc -c < "bin/uow-$arch") / 1024 + 2048 ))
free=$(df -k /overlay 2>/dev/null | awk 'NR==2 {print $4}')
[ -z "$free" ] || [ "$free" -ge "$need" ] || die "not enough flash: ${free} KB free in /overlay, ${need} KB needed"

if command -v apk >/dev/null; then
	pm_has() { apk info -e "$1" >/dev/null 2>&1; }
	pm_add() { apk add "$@"; }
	pm_update() { apk update >/dev/null; }
else
	pm_has() { opkg list-installed | grep -q "^$1 "; }
	pm_add() { opkg install "$@"; }
	pm_update() { opkg update >/dev/null; }
fi

if [ "$1" != "--no-packages" ]; then
	say "updating package lists"; pm_update
	# full hostapd (802.11k/v, EAP, runtime deny list): replace a basic wpad
	# safely: first check the full one is installable, then swap in one go
	basic=$(for p in wpad-basic-mbedtls wpad-basic-wolfssl wpad-basic-openssl wpad-basic; do pm_has $p && echo $p; done | head -1)
	if [ -n "$basic" ]; then
		full=$(echo "$basic" | sed 's/-basic//')
		say "replacing $basic with $full (Wi-Fi restarts once)"
		# from the repo only (apk refuses locally fetched files); the AP keeps
		# its cable connection while Wi-Fi is down for a moment
		if command -v apk >/dev/null; then
			apk del "$basic" && apk add "$full" || { apk add "$basic"; die "swap failed, $basic restored"; }
		else
			opkg remove "$basic" && opkg install "$full" || { opkg install "$basic"; die "swap failed, $basic restored"; }
		fi
	fi
	for p in hostapd-utils kmod-nft-bridge; do pm_has $p || pm_add $p; done
	for p in usteer alsa-utils; do pm_has $p || pm_add $p || say "optional $p not installed"; done
	/etc/init.d/usteer stop 2>/dev/null; /etc/init.d/usteer disable 2>/dev/null; true
fi

say "installing /usr/bin/uow ($arch)"
cp "bin/uow-$arch" /usr/bin/uow.new && chmod 755 /usr/bin/uow.new && mv /usr/bin/uow.new /usr/bin/uow
cp files/etc/init.d/unifi-openwrt /etc/init.d/unifi-openwrt && chmod 755 /etc/init.d/unifi-openwrt
if [ -f /etc/config/unifi-openwrt ]; then
	say "keeping the existing /etc/config/unifi-openwrt"
else
	cp files/etc/config/unifi-openwrt /etc/config/unifi-openwrt
	uci set unifi-openwrt.main.hostname="$(uci -q get system.@system[0].hostname || echo OpenWrt)"
	uci commit unifi-openwrt
fi
mkdir -p /etc/unifi-openwrt/state && chmod 700 /etc/unifi-openwrt /etc/unifi-openwrt/state

say "closest UniFi models for this AP:"
/usr/bin/uow -ap local -mac "$(cat /sys/class/net/br-lan/address)" -suggest 2>/dev/null | sed 's/^/  /'
cat <<MSG

Next:
  uci set unifi-openwrt.main.model='<code from the list above>'
  uci set unifi-openwrt.main.inform_url='http://<your UniFi console IP>:8080/inform'
  uci set unifi-openwrt.main.enabled='1'; uci commit unifi-openwrt
  /etc/init.d/unifi-openwrt enable; /etc/init.d/unifi-openwrt start
Then click Adopt in the UniFi. Logs: logread -e uow
MSG
