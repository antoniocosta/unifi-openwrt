// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

// "uow wire-probe GATEWAY TRIES IFACE...": does the gateway answer an ARP
// request sent out of one of these interfaces? Exit 0 = yes.
//
// For the mesh-uplink watcher (internal/openwrt/mesh.go), which must know
// whether a cable with link leads to the gateway while the wireless uplink is
// in the same bridge. A ping cannot tell: it leaves by whichever port the
// bridge has learned the gateway on. This request leaves by the cable only (a
// packet socket on the port sends past the bridge). The answer may come back
// through either, so it is listened for on every interface.
//
// The request is an ARP probe (sender address 0.0.0.0, RFC 5227). The owner
// of an address answers it like any request, the answer names 0.0.0.0 as its
// target, which no answer to the kernel's own requests does, and no ARP
// table takes anything in from it.
func wireProbe(args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: uow wire-probe GATEWAY TRIES IFACE...")
		return 2
	}
	gw := net.ParseIP(args[0]).To4()
	tries, err := strconv.Atoi(args[1])
	if gw == nil || err != nil || tries < 1 || tries > 10 {
		fmt.Fprintln(os.Stderr, "wire-probe: bad gateway or tries")
		return 2
	}
	const ethARP = 0x0806
	proto := uint16(ethARP>>8 | ethARP&0xff<<8) // network byte order
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, int(proto))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wire-probe:", err)
		return 2
	}
	defer syscall.Close(fd)
	syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &syscall.Timeval{Usec: 200000})

	type port struct {
		index int
		frame []byte
	}
	var ports []port
	var macs [][]byte
	for _, name := range args[2:] {
		ifi, err := net.InterfaceByName(name)
		if err != nil || len(ifi.HardwareAddr) != 6 {
			continue
		}
		ports = append(ports, port{ifi.Index, arpProbe(ifi.HardwareAddr, gw)})
		macs = append(macs, ifi.HardwareAddr)
	}
	if len(ports) == 0 {
		return 1
	}
	bcast := [8]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	buf := make([]byte, 1500)
	for t := 0; t < tries; t++ {
		for _, p := range ports {
			syscall.Sendto(fd, p.frame, 0, &syscall.SockaddrLinklayer{Protocol: proto, Ifindex: p.index, Halen: 6, Addr: bcast})
		}
		for end := time.Now().Add(time.Second); time.Now().Before(end); {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil || !arpProbeAnswer(buf[:n], gw, macs) {
				continue
			}
			return 0
		}
	}
	return 1
}

// arpProbe is an Ethernet frame with an ARP request for ip from mac, sender
// address 0.0.0.0.
func arpProbe(mac net.HardwareAddr, ip net.IP) []byte {
	f := make([]byte, 42)
	copy(f[0:6], []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	copy(f[6:12], mac)
	binary.BigEndian.PutUint16(f[12:], 0x0806)
	binary.BigEndian.PutUint16(f[14:], 1)      // Ethernet
	binary.BigEndian.PutUint16(f[16:], 0x0800) // IPv4
	f[18], f[19] = 6, 4
	binary.BigEndian.PutUint16(f[20:], 1) // request
	copy(f[22:28], mac)
	// sender address 0.0.0.0, target hardware address zero
	copy(f[38:42], ip)
	return f
}

// arpProbeAnswer reports whether f is ip's answer to an arpProbe from one of
// macs.
func arpProbeAnswer(f []byte, ip net.IP, macs [][]byte) bool {
	if len(f) < 42 || binary.BigEndian.Uint16(f[12:]) != 0x0806 || binary.BigEndian.Uint16(f[20:]) != 2 {
		return false
	}
	if !bytes.Equal(f[28:32], ip) || !bytes.Equal(f[38:42], []byte{0, 0, 0, 0}) {
		return false
	}
	for _, m := range macs {
		if bytes.Equal(f[32:38], m) {
			return true
		}
	}
	return false
}
