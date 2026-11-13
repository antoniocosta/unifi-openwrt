// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"bufio"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RF scan and neighbour list (docs/internals.md). A UAP reports, per radio
// row of radio_table:
//
//	scan_table: [{bssid, essid, channel, bw, freq, center_freq, rssi, signal,
//	              security (open|wep|wpapsk|wpaeap), age (s), band, is_unifi, serialno}]
//	spectrum_table: [{channel, width, center_freq, utilization (%), interference (dBm)}]
//	spectrum_table_time: age of spectrum_table in seconds
//
// and spectrum_scanning=true at the top level while an RF scan runs. Here
// both come from one `iw scan` per radio, plus the channel survey the scan
// leaves behind (busy/active time per channel visited).

// BSS is one neighbour network seen by a scan.
type BSS struct {
	BSSID, SSID string
	Freq        int // MHz, primary channel
	Channel     int
	Width       int // MHz: 20, 40, 80, 160
	CenterFreq  int // MHz, centre of the whole width
	Signal      int // dBm
	AgeMs       int
	Security    string
	Seen        time.Time // when a scan last returned it (zero: the result's Time)
}

// ChanSurvey is one `iw survey dump` entry.
type ChanSurvey struct {
	Freq   int
	Noise  int
	InUse  bool
	Active int64 // ms
	Busy   int64 // ms
}

// ScanResult is what one scan of one radio found.
type ScanResult struct {
	Band     string // ng / na
	Time     time.Time
	BSS      []BSS
	Spectrum []SpectrumEntry
}

// SpectrumEntry is one row of spectrum_table.
type SpectrumEntry struct {
	Channel, Width, CenterFreq, Utilization, Interference int
}

// Scan runs an active scan on an AP interface (ap-force: a UAP's RF scan
// also leaves the channel briefly) and reads the channel survey around it.
// perStep: channels per off-channel step (1 on a radio with clients: about
// 0.1 s away, then 1 s back on its channel).
func (cl *Client) Scan(iface string, perStep int) ([]BSS, []ChanSurvey, error) {
	if perStep < 1 || perStep > 8 {
		perStep = 4
	}
	if !ifaceSafe(iface) {
		return nil, nil, fmt.Errorf("scan: bad interface %q", iface)
	}
	// One full-band scan gives each 5 GHz channel too short a dwell on
	// ath10k (it found only the AP's own channel; a one-channel scan of 149
	// found the UniFi console's AP at -41 dBm, docs/internals.md). So: 4 channels at a
	// time, a short pause between (clients get the radio back).
	out, err := cl.Run(fmt.Sprintf(`IF=%[1]s
N=%[2]d
phy=$(cat /sys/class/net/$IF/phy80211/name) || exit 1
iw dev $IF survey dump; echo @@SCAN@@
set -- $(iw phy $phy info | awk '/^[ \t]+\* [0-9]+(\.[0-9]+)? MHz/ && !/disabled/ {print int($2)}')
while [ $# -gt 0 ]; do
	chunk="$1"; shift
	i=1; while [ $i -lt $N ] && [ $# -gt 0 ]; do chunk="$chunk $1"; shift; i=$((i+1)); done
	iw dev $IF scan freq $chunk ap-force 2>/dev/null || { sleep 1; iw dev $IF scan freq $chunk ap-force 2>/dev/null; }
	sleep 0.3 2>/dev/null || sleep 1
done
echo @@SURVEY@@; iw dev $IF survey dump`, iface, perStep))
	if err != nil {
		return nil, nil, fmt.Errorf("scan %s: %v (%s)", iface, err, firstLine(string(out)))
	}
	s := string(out)
	before, rest, ok1 := strings.Cut(s, "@@SCAN@@")
	scan, after, ok2 := strings.Cut(rest, "@@SURVEY@@")
	if !ok1 || !ok2 {
		return nil, nil, fmt.Errorf("scan %s: unexpected output", iface)
	}
	// each step prints the whole cache: keep the newest copy of each BSS
	// (the kernel drops entries after 30 s, so a final dump would lose the
	// first channels of a slow scan)
	all := ParseScan(scan)
	idx := map[string]int{}
	var bss []BSS
	for _, b := range all {
		if i, ok := idx[b.BSSID]; ok {
			bss[i] = b
			continue
		}
		idx[b.BSSID] = len(bss)
		bss = append(bss, b)
	}
	return bss, SurveyDelta(ParseSurvey(before), ParseSurvey(after)), nil
}

func ifaceSafe(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

// ParseScan reads `iw dev X scan` output.
func ParseScan(out string) []BSS {
	var res []BSS
	var b *BSS
	var privacy, rsn, wpa, eap bool
	var htSec string
	var vhtWidth, vhtSeg1, heWidth int
	section := ""
	flush := func() {
		if b == nil {
			return
		}
		switch {
		case rsn || wpa:
			b.Security = "wpapsk"
			if eap {
				b.Security = "wpaeap"
			}
		case privacy:
			b.Security = "wep"
		default:
			b.Security = "open"
		}
		b.Channel = freqChannel(b.Freq)
		b.Width, b.CenterFreq = 20, b.Freq
		if htSec == "above" {
			b.Width, b.CenterFreq = 40, b.Freq+10
		} else if htSec == "below" {
			b.Width, b.CenterFreq = 40, b.Freq-10
		}
		if vhtWidth >= 1 && vhtSeg1 > 0 { // 1 = 80 MHz (or 160/80+80 with seg2)
			b.Width, b.CenterFreq = 80, channelFreq(vhtSeg1, b.Freq)
		}
		if heWidth > b.Width {
			b.Width = heWidth
		}
		res = append(res, *b)
		b = nil
	}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "BSS ") {
			flush()
			mac := strings.TrimPrefix(line, "BSS ")
			if i := strings.IndexAny(mac, "( "); i > 0 {
				mac = mac[:i]
			}
			b = &BSS{BSSID: strings.ToLower(mac), AgeMs: -1}
			privacy, rsn, wpa, eap = false, false, false, false
			htSec, vhtWidth, vhtSeg1, heWidth = "", 0, 0, 0
			section = ""
			continue
		}
		if b == nil {
			continue
		}
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(line, "\t\t") && !strings.HasPrefix(t, "*") {
			section = ""
		}
		switch {
		case strings.HasPrefix(t, "freq:"):
			f, _ := strconv.ParseFloat(strings.Fields(strings.TrimPrefix(t, "freq:"))[0], 64)
			b.Freq = int(f)
		case strings.HasPrefix(t, "signal:"):
			f, _ := strconv.ParseFloat(strings.Fields(strings.TrimPrefix(t, "signal:"))[0], 64)
			b.Signal = int(math.Round(f))
		case strings.HasPrefix(t, "last seen:") && strings.HasSuffix(t, "ms ago"):
			b.AgeMs, _ = strconv.Atoi(strings.Fields(strings.TrimPrefix(t, "last seen:"))[0])
		case strings.HasPrefix(t, "SSID:"):
			b.SSID = unescapeSSID(strings.TrimPrefix(strings.TrimPrefix(t, "SSID:"), " "))
			if strings.Trim(b.SSID, "\x00") == "" {
				b.SSID = "" // hidden network
			}
		case strings.HasPrefix(t, "capability:"):
			privacy = strings.Contains(t, "Privacy")
		case strings.HasPrefix(t, "RSN:"):
			rsn, section = true, "rsn"
		case strings.HasPrefix(t, "WPA:"):
			wpa, section = true, "wpa"
		case strings.HasPrefix(t, "HT operation:"):
			section = "ht"
		case strings.HasPrefix(t, "VHT operation:"):
			section = "vht"
		case strings.HasPrefix(t, "HE Operation:") || strings.HasPrefix(t, "HE operation:"):
			section = "he"
		case strings.HasPrefix(t, "* Authentication suites:") && (section == "rsn" || section == "wpa"):
			if strings.Contains(t, "IEEE 802.1X") || strings.Contains(t, "802.1X") {
				eap = true
			}
		case strings.HasPrefix(t, "* secondary channel offset:") && section == "ht":
			htSec = strings.TrimSpace(strings.TrimPrefix(t, "* secondary channel offset:"))
		case strings.HasPrefix(t, "* channel width:") && section == "vht":
			vhtWidth, _ = strconv.Atoi(strings.Fields(strings.TrimPrefix(t, "* channel width:"))[0])
		case strings.HasPrefix(t, "* center freq segment 1:") && section == "vht":
			vhtSeg1, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(t, "* center freq segment 1:")))
		case strings.HasPrefix(t, "* center freq segment 2:") && section == "vht":
			if v, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(t, "* center freq segment 2:"))); v > 0 && vhtWidth >= 1 {
				if d := v - vhtSeg1; d == 8 || d == -8 {
					vhtSeg1 = v // 160 MHz: seg2 is the centre
					heWidth = 160
				}
			}
		}
	}
	flush()
	return res
}

// unescapeSSID undoes iw's \xNN escaping (and "\ " for spaces).
func unescapeSSID(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] == 'x' {
			if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
				b = append(b, byte(v))
				i += 3
				continue
			}
		}
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == ' ' || s[i+1] == '\\') {
			b = append(b, s[i+1])
			i++
			continue
		}
		b = append(b, s[i])
	}
	return string(b)
}

// ParseSurvey reads `iw dev X survey dump`.
func ParseSurvey(out string) []ChanSurvey {
	var res []ChanSurvey
	var c *ChanSurvey
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Survey data from"):
			if c != nil {
				res = append(res, *c)
			}
			c = &ChanSurvey{}
		case c == nil:
		case strings.HasPrefix(t, "frequency:"):
			f := strings.Fields(strings.TrimPrefix(t, "frequency:"))
			if len(f) > 0 {
				c.Freq, _ = strconv.Atoi(f[0])
			}
			c.InUse = strings.Contains(t, "[in use]")
		case strings.HasPrefix(t, "noise:"):
			f := strings.Fields(strings.TrimPrefix(t, "noise:"))
			if len(f) > 0 {
				c.Noise, _ = strconv.Atoi(f[0])
			}
		case strings.HasPrefix(t, "channel active time:"):
			c.Active = surveyMs(t)
		case strings.HasPrefix(t, "channel busy time:"):
			c.Busy = surveyMs(t)
		}
	}
	if c != nil {
		res = append(res, *c)
	}
	return res
}

func surveyMs(t string) int64 {
	_, v, _ := strings.Cut(t, ":")
	f := strings.Fields(v)
	if len(f) == 0 {
		return 0
	}
	n, _ := strconv.ParseInt(f[0], 10, 64)
	return n
}

// SurveyDelta: what the scan saw per channel. The channel in use counts
// since boot (take the difference); off-channel entries hold the last dwell
// (ath10k) or a running total (others): busy/active is right either way.
func SurveyDelta(before, after []ChanSurvey) []ChanSurvey {
	old := map[int]ChanSurvey{}
	for _, c := range before {
		old[c.Freq] = c
	}
	var res []ChanSurvey
	for _, c := range after {
		if o, ok := old[c.Freq]; ok && c.InUse && c.Active > o.Active && c.Busy >= o.Busy {
			c.Active -= o.Active
			c.Busy -= o.Busy
		}
		if c.Active > 0 {
			res = append(res, c)
		}
	}
	return res
}

// Spectrum turns per-channel survey data into spectrum_table rows: every
// 20 MHz channel, plus each 40 and 80 MHz block whose channels were all seen.
func Spectrum(sv []ChanSurvey, band string) []SpectrumEntry {
	byCh := map[int]ChanSurvey{}
	var chans []int
	for _, c := range sv {
		ch := freqChannel(c.Freq)
		if ch <= 0 || c.Active <= 0 {
			continue
		}
		if band == "ng" && c.Freq > 2500 || band == "na" && c.Freq < 4900 {
			continue
		}
		if _, dup := byCh[ch]; !dup {
			chans = append(chans, ch)
		}
		byCh[ch] = c
	}
	sort.Ints(chans)
	// no noise reading (0 dBm: ath10k off-channel on 2.4 GHz) -> the noise
	// floor measured on the channel in use, else a typical -95
	floor := -95
	for _, c := range byCh {
		if c.InUse && c.Noise < 0 {
			floor = c.Noise
		}
	}
	for ch, c := range byCh {
		if c.Noise >= 0 {
			c.Noise = floor
			byCh[ch] = c
		}
	}
	util := func(c ChanSurvey) int { return pct(c.Busy, c.Active) }
	var res []SpectrumEntry
	for _, ch := range chans {
		c := byCh[ch]
		res = append(res, SpectrumEntry{Channel: ch, Width: 20, CenterFreq: c.Freq, Utilization: util(c), Interference: c.Noise})
	}
	if band != "na" {
		return res
	}
	block := func(width int, first int) {
		n := width / 20
		var busy, act int64
		noise := -200
		for i := 0; i < n; i++ {
			c, ok := byCh[first+4*i]
			if !ok {
				return
			}
			busy, act = busy+c.Busy, act+c.Active
			if c.Noise > noise {
				noise = c.Noise
			}
		}
		res = append(res, SpectrumEntry{Channel: first, Width: width, CenterFreq: channelFreq(first, 5000) + (width-20)/2,
			Utilization: pct(busy, act), Interference: noise})
	}
	for _, first := range []int{36, 44, 52, 60, 100, 108, 116, 124, 132, 140, 149, 157, 165, 173} {
		block(40, first)
	}
	for _, first := range []int{36, 52, 100, 116, 132, 149, 165} {
		block(80, first)
	}
	return res
}

func freqChannel(f int) int {
	switch {
	case f == 2484:
		return 14
	case f >= 2412 && f < 2484:
		return (f - 2407) / 5
	case f >= 5000 && f < 5900:
		return (f - 5000) / 5
	}
	return 0
}

// channelFreq: channel number -> MHz, in the band of ref (a MHz value).
func channelFreq(ch, ref int) int {
	if ref < 3000 {
		if ch == 14 {
			return 2484
		}
		return 2407 + 5*ch
	}
	return 5000 + 5*ch
}

// ScanTable builds radio_table[].scan_table rows. own: this AP's BSSIDs
// (left out). siteSSIDs: SSIDs the UniFi serves on this AP; a neighbour on
// one of those that can't be matched to a UniFi device is left out, so the
// controller doesn't raise a rogue-AP alarm for another AP of the same
// network (a UAP knows its siblings from the Ubiquiti beacon IE; we only
// know devices we can see on the LAN). neigh: LAN neighbours (mac -> ip).
func ScanTable(r ScanResult, now time.Time, own map[string]bool, siteSSIDs map[string]bool, neigh map[string]string) []map[string]any {
	age0 := int(now.Sub(r.Time).Seconds())
	if age0 < 0 {
		age0 = 0
	}
	var rows []map[string]any
	for _, b := range r.BSS {
		if own[b.BSSID] || b.Freq == 0 {
			continue
		}
		dev := parentDevice(b.BSSID, neigh)
		if dev == "" && b.SSID != "" && siteSSIDs[b.SSID] {
			continue
		}
		age := age0
		if !b.Seen.IsZero() {
			age = max(0, int(now.Sub(b.Seen).Seconds()))
		}
		if b.AgeMs > 0 {
			age += b.AgeMs / 1000
		}
		rssi := b.Signal + 95
		if rssi < 0 {
			rssi = 0
		}
		row := map[string]any{
			"bssid": b.BSSID, "essid": b.SSID, "channel": b.Channel, "bw": b.Width,
			"freq": b.Freq, "center_freq": b.CenterFreq, "rssi": rssi, "signal": b.Signal,
			"security": b.Security, "age": age, "band": r.Band, "is_unifi": dev != "",
			"is_adhoc": false,
		}
		if dev != "" {
			row["serialno"] = dev
		}
		rows = append(rows, row)
	}
	return rows
}

// SpectrumRows: spectrum_table as inform JSON.
func SpectrumRows(sp []SpectrumEntry) []map[string]any {
	rows := make([]map[string]any, 0, len(sp))
	for _, e := range sp {
		rows = append(rows, map[string]any{"channel": e.Channel, "width": e.Width, "center_freq": e.CenterFreq,
			"utilization": e.Utilization, "interference": e.Interference})
	}
	return rows
}

// ScanIface is the daemon's listen-only interface on a spare radio (the
// OnHub's third radio: 2.4 + 5 GHz, one antenna, unused by OpenWrt's
// config). Scanning there never takes a client radio off its channel
// (docs/internals.md). The collector skips it (collectScript), so the UniFi
// doesn't see an extra radio.
const ScanIface = "uowscan0"

// EnsureScanIface creates ScanIface on a radio that has no interface at all
// (managed mode, never associated) and returns whether it exists.
func (cl *Client) EnsureScanIface() bool {
	out, err := cl.Run(`IF=` + ScanIface + `
if [ -e /sys/class/net/$IF ]; then ip link set $IF up 2>/dev/null; echo ok; exit 0; fi
used=" $(cat /sys/class/net/*/phy80211/name 2>/dev/null | tr '\n' ' ')"
for d in /sys/class/ieee80211/*; do
	p=$(basename $d)
	case "$used" in *" $p "*) continue;; esac
	iw phy $p interface add $IF type managed 2>/dev/null && ip link set $IF up 2>/dev/null && { echo ok; exit 0; }
done
echo none`)
	return err == nil && strings.TrimSpace(string(out)) == "ok"
}

// ScanSpare runs one full scan on ScanIface: every band the spare radio
// has, with the channel survey around it.
func (cl *Client) ScanSpare() ([]BSS, []ChanSurvey, error) {
	out, err := cl.Run("iw dev " + ScanIface + " survey dump; echo @@SCAN@@; iw dev " + ScanIface + " scan || { sleep 2; iw dev " + ScanIface + " scan; }; echo @@SURVEY@@; iw dev " + ScanIface + " survey dump")
	if err != nil {
		return nil, nil, fmt.Errorf("scan %s: %v (%s)", ScanIface, err, firstLine(string(out)))
	}
	before, rest, ok1 := strings.Cut(string(out), "@@SCAN@@")
	scan, after, ok2 := strings.Cut(rest, "@@SURVEY@@")
	if !ok1 || !ok2 {
		return nil, nil, fmt.Errorf("scan %s: unexpected output", ScanIface)
	}
	return ParseScan(scan), SurveyDelta(ParseSurvey(before), ParseSurvey(after)), nil
}

// SplitBands sorts scan results by band (ng: 2.4 GHz, na: 5 GHz).
func SplitBands(bss []BSS, sv []ChanSurvey, now time.Time) map[string]ScanResult {
	res := map[string]ScanResult{}
	for _, band := range []string{"ng", "na"} {
		r := ScanResult{Band: band, Time: now}
		for _, b := range bss {
			if (b.Freq < 3000) == (band == "ng") && b.Freq > 0 && b.Freq < 5900 {
				r.BSS = append(r.BSS, b)
			}
		}
		r.Spectrum = Spectrum(sv, band)
		if len(r.BSS) > 0 || len(r.Spectrum) > 0 {
			res[band] = r
		}
	}
	return res
}

// KeepSeen carries BSSes of an older result into a newer one when the new
// scan missed them (a 46 ms dwell does, now and then) and they were seen
// within keep. Their age keeps growing: the controller only lists rows
// younger than 30 s, the 802.11k neighbour list uses them all.
func KeepSeen(old, cur ScanResult, keep time.Duration) ScanResult {
	have := map[string]bool{}
	for i := range cur.BSS {
		if cur.BSS[i].Seen.IsZero() {
			cur.BSS[i].Seen = cur.Time
		}
		have[cur.BSS[i].BSSID] = true
	}
	for _, b := range old.BSS {
		if b.Seen.IsZero() {
			b.Seen = old.Time
		}
		if !have[b.BSSID] && cur.Time.Sub(b.Seen) < keep {
			cur.BSS = append(cur.BSS, b)
		}
	}
	return cur
}
