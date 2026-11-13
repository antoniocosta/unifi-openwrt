## Packages (OpenWrt 25.12+, apk)

OpenWrt 25.12 and later use apk-tools 3 and `.apk` packages. There are two ways to
get a package: build it from the prebuilt binaries (quick, no SDK), or build it in
the OpenWrt SDK / add it to a feed.

### Build from the prebuilt binaries

```sh
tools/build.sh            # cross-compile dist/openwrt/bin/uow-{armv7,aarch64,mipsel,mips,x86_64}
tools/mkapk.sh --get-apk  # once: fetch a static apk-tools 3 (Alpine) to ~/.local/opt/apk-tools/bin
tools/mkapk.sh            # packages for common arches, into dist/apk/
tools/mkapk.sh arm_cortex-a15_neon-vfpv4   # or only the arch(es) you need
```

`tools/mkapk.sh` needs an apk-tools 3 that has `mkpkg` (the `apk` on OpenWrt devices
does not). It looks at `$APK`, then `~/.local/opt/apk-tools/bin/apk`, then `apk` in
`PATH`. `--get-apk` (x86_64 hosts) downloads Alpine's `apk-tools-static` and checks
it against the Alpine package index before installing it.

apk installs a package only if its arch is listed in the device's `/etc/apk/arch`, so
there is one package per OpenWrt arch. Find yours with `cat /etc/apk/arch` on the
device. The packages are small (about 3.5 MB); a single package holding every binary
would be about 40 MB, too big for most flash. Arches without a matching binary
(ARMv5/v6, ARM cores without an FPU, mips64, i386) are skipped; use the SDK route for
those.

Output: `dist/apk/unifi-openwrt_<version>_<arch>.apk`. The version comes from
`packaging/openwrt/unifi-openwrt/Makefile` (set `VERSION=` to override).

### Install

Copy the package for your arch to the device, then:

```sh
apk update
apk add --allow-untrusted ./unifi-openwrt_*.apk
```

`--allow-untrusted` is needed because the package is not signed with a key in
`/etc/apk/keys`. apk installs the dependencies (`hostapd-utils`, `kmod-nft-bridge`,
`iw`, `nftables`) from the OpenWrt repositories, so run `apk update` first.

After installing, the service is installed but does nothing: `option enabled '0'` in
`/etc/config/unifi-openwrt` is the default. To see which UniFi models suit the AP, run:

```sh
uow -ap local -mac "$(cat /sys/class/net/br-lan/address)" -suggest
```

Then turn it on:

```sh
uci set unifi-openwrt.main.model='<code from the list>'
uci set unifi-openwrt.main.inform_url='http://192.168.1.1:8080/inform'   # your controller
uci set unifi-openwrt.main.enabled='1'; uci commit unifi-openwrt
/etc/init.d/unifi-openwrt start
```

and click Adopt in the UniFi controller. Logs: `logread -e uow`.

A full hostapd is recommended (802.11k/v, WPA-Enterprise, runtime deny list), but the
package does not require one. Every wpad/hostapd variant, basic or full, provides the
same virtual names, so a dependency could only name one concrete variant. That would
choose the TLS library for you and would conflict with the `wpad-basic-*` that images
ship. If a basic variant is installed, the install script says so. To swap it (Wi-Fi
restarts, so do this over a cable):

```sh
apk del wpad-basic-mbedtls && apk add wpad-mbedtls
```

Optional extras: `usteer` (band steering), `alsa-utils` (Locate beep), `snmpd`.

The package keeps `/etc/config/unifi-openwrt` as a conffile. It adds
`/etc/unifi-openwrt/` (adoption state, including the device key) to sysupgrade's keep
list, so an upgraded AP stays adopted.

### Upgrade and remove

```sh
apk add --allow-untrusted ./unifi-openwrt_<newer>.apk   # upgrade; a running daemon restarts
apk del unifi-openwrt                                    # stops and disables the service
rm -rf /etc/unifi-openwrt                                # optional: forget the adoption state
```

Removing the package leaves the Wi-Fi settings the controller applied in
`/etc/config/wireless` as they are. Forget the device in the UniFi controller too.

If unifi-openwrt was installed earlier with `dist/openwrt/install.sh`, the package
takes over `/usr/bin/uow` and `/etc/init.d/unifi-openwrt`. apk protects files under
`/etc`, so an existing `/etc/config/unifi-openwrt` that differs from the packaged one is
kept, and the package's version is saved next to it as `.apk-new`.

### OpenWrt SDK or feed

`packaging/openwrt/unifi-openwrt/Makefile` is a standard OpenWrt package Makefile
(`golang-package.mk` from the packages feed; needs golang 1.26, which the
openwrt-25.12 packages feed has). It builds `cmd/uow` for the SDK's target, with the
right `GOARM`/`GOMIPS` for every arch, and makes a signed package when the SDK or
buildroot has signing keys. In the SDK:

```sh
echo "src-link unifi_openwrt $PWD/../unifi-openwrt/packaging/openwrt" >> feeds.conf.default
./scripts/feeds update packages unifi_openwrt
./scripts/feeds install unifi-openwrt
make defconfig
make package/unifi-openwrt/compile V=s
# result: bin/packages/<arch>/unifi_openwrt/unifi-openwrt-<version>.apk
```

By default the Makefile builds the source tree it sits in. Set `UOW_SRC_DIR=` to build
another checkout. For a published feed, switch to the commented `PKG_SOURCE_*` lines.
Its dependencies are `hostapd-utils` and `kmod-nft-bridge`. `iw` and `nftables` come
with default images and are left out, because Kconfig cannot express "iw or iw-full".

## Tested

On one device only (OpenWrt 25.12, `arm_cortex-a15_neon-vfpv4`, apk-tools 3.0.5):
installing over an install.sh setup (apk takes over `/usr/bin/uow` and the init script,
the existing `/etc/config/unifi-openwrt` is kept and the package default lands in
`unifi-openwrt.apk-new`, the running daemon restarts), `apk del` (service stopped and
disabled, config and `/etc/unifi-openwrt` kept) and reinstalling (service enabled and
started again, adoption kept). The other architectures are built the same way but untested.
The SDK/feed Makefile has not been built yet.
