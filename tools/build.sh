#!/bin/sh
# SPDX-License-Identifier: GPL-2.0-or-later
# Cross-compile the daemon for common OpenWrt targets into dist/openwrt/bin/ and
# assemble dist/openwrt/ (install.sh, files/, bin/): copy that folder to an AP.
set -e
cd "$(dirname "$0")/.."
export GOTOOLCHAIN=local CGO_ENABLED=0
mkdir -p dist/openwrt/bin
b() { GOOS=linux GOARCH=$1 GOARM=$2 GOMIPS=$3 go build -trimpath -ldflags='-s -w' -o dist/openwrt/bin/uow-$4 ./cmd/uow; echo "built uow-$4"; }
b arm 7 "" armv7      # ipq806x, ipq40xx, mvebu, bcm27xx...
b arm64 "" "" aarch64 # mediatek filogic, ipq807x, rockchip...
b mipsle "" softfloat mipsel # ramips mt7621/mt76x8
b mips "" softfloat mips     # ath79
b amd64 "" "" x86_64
cp packaging/install.sh dist/openwrt/install.sh
rm -rf dist/openwrt/files && cp -R packaging/files dist/openwrt/files
echo "installer bundle: dist/openwrt/"
