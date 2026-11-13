// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
)

// RF scan and neighbour list (docs/internals.md). The UniFi's "scan" and
// "spectrum-scan" commands scan every radio; in between, a background scan
// every 15 minutes refreshes the neighbour list, only on radios with no
// clients and no mesh uplink.

const bgScanEvery = 15 * time.Minute

type scanner struct {
	mu       sync.Mutex
	running  bool
	spectrum bool // an RF scan (spectrum-scan) is running
	results  map[string]openwrt.ScanResult
	lastBG   time.Time
	snap     *openwrt.Snapshot // last AP reading (from the inform)
	lastRRM  time.Time
	rrmErr   string
}

var scans = &scanner{results: map[string]openwrt.ScanResult{}}

// start scans in the background; force = asked for by the controller (all
// radios, clients or not).
func (sc *scanner) start(ap *openwrt.Client, force, spectrum bool) {
	sc.mu.Lock()
	if sc.running {
		sc.spectrum = sc.spectrum || spectrum
		sc.mu.Unlock()
		return
	}
	sc.running, sc.spectrum = true, spectrum
	sc.mu.Unlock()
	go func() {
		defer func() {
			sc.mu.Lock()
			sc.running, sc.spectrum = false, false
			sc.lastBG = time.Now()
			sc.mu.Unlock()
			sc.syncRRM(ap)
		}()
		// a spare radio scans without touching the client radios
		if ap.EnsureScanIface() {
			t0 := time.Now()
			bss, sv, err := ap.ScanSpare()
			if err == nil && len(bss) > 0 {
				res := openwrt.SplitBands(bss, sv, time.Now())
				sc.mu.Lock()
				for band, r := range res {
					sc.results[band] = openwrt.KeepSeen(sc.results[band], r, time.Hour)
				}
				sc.mu.Unlock()
				log.Printf("scan (spare radio %s): %d networks, %d + %d spectrum rows in %s", openwrt.ScanIface, len(bss),
					len(res["ng"].Spectrum), len(res["na"].Spectrum), time.Since(t0).Round(100*time.Millisecond))
				return
			}
			log.Printf("scan on the spare radio failed (%v, %d networks): using the AP radios", err, len(bss))
		}
		s, err := ap.Collect()
		if err != nil {
			log.Printf("scan: reading the AP: %v", err)
			return
		}
		for _, r := range s.Radios {
			iface, busy := "", false
			for _, v := range r.VAPs {
				if v.Mode == "Client" {
					busy = true // mesh uplink on this radio
					continue
				}
				if iface == "" && v.Mode == "Master" {
					iface = v.Iface
				}
				if len(v.Stations) > 0 {
					busy = true
				}
			}
			// Never scan a radio with clients (or the mesh uplink) in the
			// background: even one channel at a time, 0.1 s away per second,
			// made clients leave for another AP in testing (docs/internals.md).
			if iface == "" || (busy && !force) {
				continue
			}
			t0 := time.Now()
			bss, sv, err := ap.Scan(iface, 4)
			if err != nil {
				log.Printf("scan %s: %v", r.Band, err)
				continue
			}
			res := openwrt.ScanResult{Band: r.Band, Time: time.Now(), BSS: bss, Spectrum: openwrt.Spectrum(sv, r.Band)}
			sc.mu.Lock()
			if old, ok := sc.results[r.Band]; ok && len(res.Spectrum) == 0 {
				res.Spectrum = old.Spectrum // keep the last RF scan if this one gave no survey
			}
			sc.results[r.Band] = res
			sc.mu.Unlock()
			if force || r.Band == "na" {
				log.Printf("scan %s (%s): %d networks, %d spectrum rows in %s", r.Band, iface, len(bss), len(res.Spectrum), time.Since(t0).Round(100*time.Millisecond))
			}
		}
	}()
}

// background: called each loop; starts a gentle scan when due.
func (sc *scanner) background(ap *openwrt.Client) {
	sc.mu.Lock()
	due := !sc.running && time.Since(sc.lastBG) >= bgScanEvery
	rrmDue := !sc.running && time.Since(sc.lastRRM) >= 5*time.Minute
	sc.mu.Unlock()
	if due {
		sc.start(ap, false, false)
	} else if rrmDue {
		sc.syncRRM(ap)
	}
}

// syncRRM puts the sibling APs found by the scans into each AP interface's
// 802.11k neighbour list (docs/internals.md); hostapd forgets it on restart.
func (sc *scanner) syncRRM(ap *openwrt.Client) {
	sc.mu.Lock()
	s := sc.snap
	var res []openwrt.ScanResult
	for _, r := range sc.results {
		res = append(res, r)
	}
	sc.lastRRM = time.Now()
	sc.mu.Unlock()
	if s == nil || len(res) == 0 {
		return
	}
	own := map[string]bool{}
	for _, r := range s.Radios {
		for _, v := range r.VAPs {
			own[v.BSSID] = true
		}
	}
	var errs []string
	for _, r := range s.Radios {
		for _, v := range r.VAPs {
			if v.Mode != "Master" || v.SSID == "" {
				continue
			}
			nb := append(openwrt.OwnOtherBands(s, v.SSID, v.Iface), openwrt.Siblings(res, v.SSID, own, s.ARP)...)
			changed, err := ap.SyncNeighbors(v.Iface, nb)
			if err != nil {
				errs = append(errs, err.Error())
				continue
			}
			if changed {
				log.Printf("802.11k: %s (%s) neighbour list: %d BSS", v.Iface, v.SSID, len(nb))
			}
		}
	}
	e := strings.Join(errs, "; ")
	sc.mu.Lock()
	if e != sc.rrmErr && e != "" {
		log.Printf("802.11k: %s", e)
	}
	sc.rrmErr = e
	sc.mu.Unlock()
}

// fields merges scan results into the inform fields.
func (sc *scanner) fields(f map[string]any, s *openwrt.Snapshot, info openwrt.CfgInfo) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	f["spectrum_scanning"] = sc.spectrum
	sc.snap = s
	rows, _ := f["radio_table"].([]map[string]any)
	if len(rows) == 0 || s == nil {
		return
	}
	own := map[string]bool{}
	for _, r := range s.Radios {
		own[r.BSSID] = true
		for _, v := range r.VAPs {
			own[v.BSSID] = true
		}
	}
	site := map[string]bool{}
	for ssid := range info.IDs {
		site[ssid] = true
	}
	now := time.Now()
	for _, row := range rows {
		band, _ := row["radio"].(string)
		res, ok := sc.results[band]
		if !ok {
			continue
		}
		row["scan_table"] = openwrt.ScanTable(res, now, own, site, s.ARP)
		if len(res.Spectrum) > 0 {
			row["spectrum_table"] = openwrt.SpectrumRows(res.Spectrum)
			row["spectrum_table_time"] = int(now.Sub(res.Time).Seconds())
		}
	}
}

// rrmSoon asks for a neighbour-list sync on the next loop (after a wifi
// reload hostapd has forgotten the list).
func (sc *scanner) rrmSoon() {
	sc.mu.Lock()
	sc.lastRRM = time.Time{}
	sc.mu.Unlock()
}
