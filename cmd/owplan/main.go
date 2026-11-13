// SPDX-License-Identifier: GPL-2.0-or-later

// owplan prints the UCI changes that would make an OpenWrt AP match the
// controller's system_cfg. Read-only: it never applies anything.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

func main() {
	addr := flag.String("addr", "192.168.1.2:22", "AP ssh address")
	user := flag.String("user", "root", "ssh user")
	key := flag.String("key", "ap_ed25519", "ssh private key for the AP")
	kh := flag.String("known-hosts", "ap_known_hosts", "known_hosts file with the AP's host key")
	cfgPath := flag.String("cfg", "system_cfg.txt", "system_cfg from the controller (saved by the daemon in its state directory)")
	flag.Parse()
	cfg, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	cl := openwrt.NewClient(openwrt.Target{Addr: *addr, User: *user, KeyFile: *key, KnownHosts: *kh})
	defer cl.Close()
	out, err := cl.Run("uci show wireless; uci show system; uci show network")
	if err != nil {
		log.Fatal(err)
	}
	f := cl.Features()
	f.APAddr, _, _ = net.SplitHostPort(*addr)
	fmt.Printf("AP hostapd features: %+v\n", f)
	r := translate.Plan(translate.ParseCfg(string(cfg)), translate.ParseUCI(string(out)), f)
	fmt.Printf("%d change(s):\n", len(r.Ops))
	for _, op := range r.Ops {
		fmt.Println("  " + op.String())
	}
	fmt.Println("notes:")
	for _, n := range r.Notes {
		fmt.Println("  " + n)
	}
	fmt.Printf("not translated (yet): %v\n", r.Skipped)
}
