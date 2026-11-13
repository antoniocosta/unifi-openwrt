#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Build installable OpenWrt .apk packages (apk-tools v3) from the prebuilt
# binaries in dist/openwrt/bin (tools/build.sh), without the OpenWrt SDK.
#
#   tools/mkapk.sh [OPENWRT_ARCH...]     default: a set of common arches
#   tools/mkapk.sh --get-apk             fetch a static apk-tools v3 (Alpine)
#
# apk only installs a package whose arch is listed in the device's
# /etc/apk/arch, so there is one package per OpenWrt arch (cat /etc/apk/arch
# on the device to see it). Many OpenWrt arches share one Go binary; the table
# in go_bin() picks it. A single "noarch" package with every binary would be
# ~40 MB, too big for most flash.
#
# Environment: APK (apk-tools v3 binary with mkpkg), VERSION (default from
# packaging/openwrt/unifi-openwrt/Makefile), OUT (default dist/apk),
# SOURCE_DATE_EPOCH (file mtimes, for reproducible packages).
# Output: $OUT/unifi-openwrt_<version>_<arch>.apk, unsigned: install with
#   apk add --allow-untrusted ./unifi-openwrt_<version>_<arch>.apk
set -e
cd "$(dirname "$0")/.."
top=$(pwd)
mk=packaging/openwrt/unifi-openwrt/Makefile
pkg=unifi-openwrt
APK_STATIC_VER=3.0.9-r0
APK_STATIC_DIR=$HOME/.local/opt/apk-tools/bin

die() { echo "mkapk: $*" >&2; exit 1; }

# Alpine's apk-tools-static (x86_64 hosts). The download goes into its own
# new directory and is checked against the index before anything is used.
get_apk() {
	[ "$(uname -m)" = x86_64 ] || die "--get-apk knows x86_64 hosts only; build apk-tools 3 from source"
	base=https://dl-cdn.alpinelinux.org/alpine/edge/main/x86_64
	dl=$(mktemp -d); trap 'rm -rf "$dl"' EXIT
	mkdir "$dl/index" "$dl/pkg" "$dl/x"
	curl -fsS -o "$dl/index/APKINDEX.tar.gz" "$base/APKINDEX.tar.gz"
	tar -xzf "$dl/index/APKINDEX.tar.gz" -C "$dl/index" APKINDEX 2>/dev/null
	want=$(awk -v RS= -v v="$APK_STATIC_VER" '$0 ~ "\nP:apk-tools-static\n" && $0 ~ "\nV:" v "\n"' "$dl/index/APKINDEX" | sed -n 's/^C://p')
	[ -n "$want" ] || die "apk-tools-static $APK_STATIC_VER is no longer in Alpine edge; set APK_STATIC_VER"
	curl -fsS -o "$dl/pkg/p.apk" "$base/apk-tools-static-$APK_STATIC_VER.apk"
	# an apk v2 file is three gzip streams: signature, control, data; the
	# index holds the SHA-1 of the control stream, which holds the data's SHA-256
	got=$(python3 -I -c '
import sys, zlib, hashlib, base64
b = open(sys.argv[1], "rb").read(); segs = []; off = 0
while off < len(b):
    d = zlib.decompressobj(31); d.decompress(b[off:])
    n = len(b) - off - len(d.unused_data); segs.append(b[off:off + n]); off += n
print("Q1" + base64.b64encode(hashlib.sha1(segs[1]).digest()).decode(), hashlib.sha256(segs[2]).hexdigest())
' "$dl/pkg/p.apk")
	[ "${got%% *}" = "$want" ] || die "control checksum mismatch"
	tar -xzf "$dl/pkg/p.apk" -C "$dl/x" .PKGINFO sbin/apk.static 2>/dev/null
	[ "$(sed -n 's/^datahash = //p' "$dl/x/.PKGINFO")" = "${got#* }" ] || die "data checksum mismatch"
	mkdir -p "$APK_STATIC_DIR"
	install -m 755 "$dl/x/sbin/apk.static" "$APK_STATIC_DIR/apk"
	"$APK_STATIC_DIR/apk" --version
}

find_apk() {
	for a in "$APK" "$APK_STATIC_DIR/apk" "$(command -v apk 2>/dev/null)"; do
		[ -n "$a" ] && [ -x "$a" ] || continue
		case "$("$a" --version 2>/dev/null)" in "apk-tools 3."*) ;; *) continue ;; esac
		"$a" mkpkg 2>&1 | grep -q "not an apk command" && continue
		echo "$a"; return
	done
	die "no apk-tools v3 with mkpkg found; run tools/mkapk.sh --get-apk, or set APK="
}

# OpenWrt arch -> Go binary from tools/build.sh (empty: not supported)
go_bin() {
	case "$1" in
	aarch64_*) echo aarch64 ;;
	# GOARM=7 needs an FPU (VFPv3+); FPU-less and ARMv5/v6 cores are out
	arm_cortex-a*vfp*|arm_cortex-a*neon*) echo armv7 ;;
	mipsel_24kc*|mipsel_74kc*|mipsel_mips32*) echo mipsel ;;
	mips_24kc*|mips_4kec*|mips_mips32*) echo mips ;;
	x86_64) echo x86_64 ;;
	esac
}

[ "$1" = --get-apk ] && { get_apk; exit; }
APK=$(find_apk)

[ -f "$mk" ] || die "missing $mk"
mkvar() { sed -n "s/^$1:=//p" "$mk"; }
VERSION=${VERSION:-$(mkvar PKG_VERSION)-r$(mkvar PKG_RELEASE)}
OUT=${OUT:-dist/apk}
mkdir -p "$OUT"; OUT=$(cd "$OUT" && pwd)

# the Makefile's postinst/postrm blocks, with make's $$ turned back into $
mkblock() { sed -n "/^define Package\/$pkg\/$1\$/,/^endef\$/p" "$mk" | sed '1d;$d;/^#!/d;s/\$\$/$/g'; }

[ $# -gt 0 ] || set -- arm_cortex-a7_neon-vfpv4 arm_cortex-a9_vfpv3-d16 arm_cortex-a15_neon-vfpv4 \
	arm_cortex-a53_neon-vfpv4 aarch64_cortex-a53 aarch64_cortex-a72 aarch64_generic \
	mipsel_24kc mips_24kc x86_64

work=$(mktemp -d); trap 'rm -rf "$work"' EXIT
umask 022

# apk records owner names, resolved through <root>/etc/passwd: map our
# uid/gid to root so the packaged files are root:root without fakeroot
mkdir -p "$work/ids/etc"
echo "root:x:$(id -u):$(id -g)::/root:/bin/sh" > "$work/ids/etc/passwd"
echo "root:x:$(id -g):" > "$work/ids/etc/group"

# OpenWrt's generated scripts (include/package-pack.mk), then ours
hdr='[ -s ${IPKG_INSTROOT}/lib/functions.sh ] || exit 0
. ${IPKG_INSTROOT}/lib/functions.sh
export root="${IPKG_INSTROOT}"
export pkgname="'$pkg'"'
{ echo '#!/bin/sh'; echo '[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0'; echo "$hdr"
  echo add_group_and_user; echo default_postinst; mkblock postinst; } > "$work/post-install"
{ echo '#!/bin/sh'; echo 'export PKG_UPGRADE=1'; sed 1d "$work/post-install"; } > "$work/post-upgrade"
{ echo '#!/bin/sh'; echo "$hdr"; echo default_prerm; } > "$work/pre-deinstall"
{ echo '#!/bin/sh'; mkblock postrm; } > "$work/post-deinstall"
for s in post-install post-upgrade pre-deinstall post-deinstall; do sh -n "$work/$s" || die "bad $s script"; done

# the data tree shared by every arch (the binary is added per arch)
r=$work/root
mkdir -p "$r/usr/bin" "$r/etc/init.d" "$r/etc/config" "$r/lib/upgrade/keep.d" "$r/lib/apk/packages"
install -m 755 packaging/files/etc/init.d/unifi-openwrt "$r/etc/init.d/unifi-openwrt"
install -m 644 packaging/files/etc/config/unifi-openwrt "$r/etc/config/unifi-openwrt"
# conffiles the package does not ship go to sysupgrade's keep list
echo /etc/unifi-openwrt/ > "$r/lib/upgrade/keep.d/$pkg"
echo /etc/config/unifi-openwrt > "$work/conffiles"
desc=$(sed -n "/^define Package\/$pkg\/description\$/,/^endef\$/p" "$mk" | sed '1d;$d' | tr -s ' \t\n' ' ' | sed 's/^ //;s/ $//')

built=0
for arch; do
	b=$(go_bin "$arch")
	[ -n "$b" ] || { echo "mkapk: skip $arch: no matching Go binary (see go_bin in $0)" >&2; continue; }
	bin=dist/openwrt/bin/uow-$b
	[ -f "$bin" ] || die "missing $bin; run tools/build.sh"
	install -m 755 "$bin" "$r/usr/bin/uow"
	p=$r/lib/apk/packages
	rm -f "$p/$pkg.list" "$p/$pkg.conffiles" "$p/$pkg.conffiles_static"
	(cd "$r" && find . \( -type f -o -type l \) | sed 's|^\.||' | sort) > "$work/list"
	mv "$work/list" "$p/$pkg.list"
	cp "$work/conffiles" "$p/$pkg.conffiles"
	while read -r f; do
		[ -f "$r$f" ] && echo "$f $(sha256sum < "$r$f" | cut -d' ' -f1)"
	done < "$work/conffiles" > "$p/$pkg.conffiles_static"
	chmod 644 "$p"/*
	[ -z "$SOURCE_DATE_EPOCH" ] || find "$r" -exec touch -h -d "@$SOURCE_DATE_EPOCH" {} +
	f=$OUT/${pkg}_${VERSION}_$arch.apk
	"$APK" --root "$work/ids" mkpkg \
		--info "name:$pkg" \
		--info "version:$VERSION" \
		--info "description:$desc" \
		--info "arch:$arch" \
		--info "license:$(mkvar PKG_LICENSE)" \
		--info "origin:$pkg" \
		--info "depends:hostapd-utils kmod-nft-bridge iw nftables" \
		--script "post-install:$work/post-install" \
		--script "post-upgrade:$work/post-upgrade" \
		--script "pre-deinstall:$work/pre-deinstall" \
		--script "post-deinstall:$work/post-deinstall" \
		--files "$r" \
		--output "$f"
	echo "built ${f#"$top"/} (uow-$b)"
	built=$((built + 1))
done
[ "$built" -gt 0 ] || die "nothing built"
