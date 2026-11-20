// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Apple AirPort base stations (NetBSD-based 7.x firmware) reached over the
// debug SSH login. Read-only: the daemon reports the base station to the
// controller (radios, WLANs, clients, uplink) but applies nothing to it; its
// Wi-Fi stays configured over ACP (AirPort Utility, airportctl).
//
// Measured on an AirPort Time Capsule A1470 (802.11ac), firmware 7.9.1:
// NetBSD 6.0 evbarm, two Broadcom BCM4360 (bwl0 = 5 GHz, bwl1 = 2.4 GHz) with
// net80211-style VAPs (wlanN) on top, one bridge (bridge0) in bridge mode.
// The userland has sh, sed, sort and tail but no grep, awk or head.

// PlatformAirPort is Target.Platform for these base stations.
const PlatformAirPort = "airport"

// airportScript prints sections "=== name" followed by their content.
const airportScript = `PATH=/sbin:/usr/sbin:/bin:/usr/bin
echo "=== sys"
echo "release=$(uname -r)"; echo "hostname=$(hostname)"; echo "now=$(date +%s)"
echo "boottime=$(sysctl -n kern.boottime)"; echo "physmem=$(sysctl -n hw.physmem)"
echo "loadavg=$(sysctl -n vm.loadavg)"; echo "cp_time=$(sysctl -n kern.cp_time)"
echo "model=$(/usr/bin/acp syAM 2>/dev/null)"
echo "=== vmstat"; vmstat -s
echo "=== lan"; ifconfig bridge0
echo "=== route"; netstat -rn -f inet
echo "=== arp"; arp -an
echo "=== fdb"; brconfig bridge0
echo "=== phy"; /usr/bin/acp rpc acpd.system.show
echo "=== netstat"; netstat -ibn
echo "=== netpkts"; netstat -in
for i in $(ifconfig -l); do
  case $i in wlan*) ;; *) continue;; esac
  echo "=== iface $i"; ifconfig $i
  echo "=== sta $i"; ifconfig $i list sta
  echo "=== noise $i"; wl -i $i noise
  echo "=== assoc $i"
  for m in $(wl -i $i assoclist | while read a b; do echo $b; done); do echo "--- $m"; wl -i $i sta_info $m; done
done
`

var (
	// ssid HomeNet channel 6 (2437 Mhz 11g ht/20) bssid 02:aa:00:00:00:02
	// ssid "Apple Network 0000aa" channel 136 (5680 Mhz 11a vht/80) bssid ...
	apIfRE   = regexp.MustCompile(`ssid ("[^"]*"|\S+) channel (\d+) \((\d+) Mhz ([^)]*)\)(?: bssid ([0-9a-fA-F:]{17}))?`)
	apTxRE   = regexp.MustCompile(`txpower ([0-9.]+)`)
	apPhyRE  = regexp.MustCompile(`PHY \(0x[0-9A-Fa-f]+\) index (\d+)\s+Link State - Current\s+\(link state ([^)]*)\)`)
	apSpdRE  = regexp.MustCompile(`(\d+)Mbps (Full|Half)`)
	apPortRE = regexp.MustCompile(`(?m)^\s+(\S+) flags=\S+\s+port (\d+) priority`)
	apFdbRE  = regexp.MustCompile(`(?m)^\s+([0-9a-f:]{17}) (\S+) (\d+) flags=`)
	apToRE   = regexp.MustCompile(`timeout: (\d+)`)
)

// ParseAirPort turns airportScript output into a Snapshot.
func ParseAirPort(out []byte, now time.Time) (*Snapshot, error) {
	secs := sections(out)
	s := &Snapshot{Time: now, ARP: map[string]string{}}

	sys := keyvals(secs.get("sys"))
	if sys["release"] == "" {
		return nil, fmt.Errorf("airport: no system section (is this an AirPort with the debug SSH login?)")
	}
	s.Hostname, s.Release = sys["hostname"], "AirPort NetBSD "+sys["release"]
	s.Model = "Apple AirPort"
	if m := strings.TrimSpace(sys["model"]); m != "" {
		s.Model = "Apple " + strings.TrimPrefix(strings.TrimPrefix(m, "syAM="), "syAM = ")
	}
	if b, n := atoi(sys["boottime"]), atoi(sys["now"]); b > 0 && n >= b {
		s.Uptime = n - b
	}
	for i, f := range strings.Fields(sys["loadavg"]) {
		if i < 3 {
			s.Load[i], _ = strconv.ParseFloat(f, 64)
		}
	}
	// cp_time=user = 89, nice = 0, sys = 211, intr = 368, idle = 14863
	for _, part := range strings.Split(sys["cp_time"], ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		n := atoi(strings.TrimSpace(v))
		s.CPUTotal += n
		if strings.TrimSpace(k) == "idle" {
			s.CPUIdle += n
		}
	}
	s.MemTotal = atoi(sys["physmem"])
	page := int64(4096)
	for _, line := range strings.Split(string(secs.get("vmstat")), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		n, rest := atoi(f[0]), strings.Join(f[1:], " ")
		switch rest {
		case "bytes per page":
			page = n
		case "pages free":
			s.MemFree = n * page
		case "cached file pages", "cached executable pages":
			s.MemCache += n * page
		}
	}

	for _, line := range strings.Split(string(secs.get("lan")), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 2 && f[0] == "ether":
			s.MAC = strings.ToLower(f[1])
		case len(f) >= 4 && f[0] == "inet" && f[2] == "netmask" && s.IP == "" && !strings.HasPrefix(f[1], "169.254.") && f[1] != "0.0.0.0":
			s.IP = f[1]
			if m, err := strconv.ParseUint(strings.TrimPrefix(f[3], "0x"), 16, 32); err == nil {
				s.Netmask = net.IPv4(byte(m>>24), byte(m>>16), byte(m>>8), byte(m)).String()
			}
		}
	}
	for _, line := range strings.Split(string(secs.get("route")), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "default" {
			s.Gateway = f[1]
			break
		}
	}
	// ? (192.0.2.1) at 2:bb:0:0:0:1 on bridge0
	for _, line := range strings.Split(string(secs.get("arp")), "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[2] == "at" && strings.Count(f[3], ":") == 5 {
			s.ARP[normMAC(f[3])] = strings.Trim(f[1], "()")
		}
	}

	// Bridge ports and the forwarding table (the number after the interface
	// counts down from the cache timeout).
	fdb := string(secs.get("fdb"))
	brPort := map[string]int{}
	for _, m := range apPortRE.FindAllStringSubmatch(fdb, -1) {
		brPort[m[1]] = int(atoi(m[2]))
	}
	timeout := int64(1200)
	if m := apToRE.FindStringSubmatch(fdb); m != nil {
		timeout = atoi(m[1])
	}
	for _, m := range apFdbRE.FindAllStringSubmatch(fdb, -1) {
		s.FDB = append(s.FDB, FDBEntry{MAC: m[1], Port: brPort[m[2]], Age: max(0, timeout-atoi(m[3]))})
	}
	s.FDB = append(s.FDB, FDBEntry{MAC: s.MAC, Local: true})

	// Interface byte counters; netstat cuts names to five characters and
	// repeats an interface per address, so the largest values win.
	type ctr struct{ in, out int64 }
	bytesOf := map[string]ctr{}
	for _, line := range strings.Split(string(secs.get("netstat")), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[2] != "<Link>" {
			continue
		}
		c := ctr{atoi(f[len(f)-2]), atoi(f[len(f)-1])}
		if o := bytesOf[f[0]]; c.in+c.out >= o.in+o.out {
			bytesOf[f[0]] = c
		}
	}
	// Packets and errors per interface (same shape: Ipkts Ierrs Opkts Oerrs Colls).
	type pk struct{ in, inErr, out, outErr int64 }
	pktsOf := map[string]pk{}
	for _, line := range strings.Split(string(secs.get("netpkts")), "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[2] != "<Link>" {
			continue
		}
		n := len(f)
		c := pk{atoi(f[n-5]), atoi(f[n-4]), atoi(f[n-3]), atoi(f[n-2])}
		if o := pktsOf[f[0]]; c.in+c.out >= o.in+o.out {
			pktsOf[f[0]] = c
		}
	}
	short := func(name string) string {
		if len(name) > 5 {
			return name[:5]
		}
		return name
	}

	// Ethernet: one port per switch PHY (index 0 is the WAN jack). The
	// switch has one counter set (bcmeth0) and one bridge port for all of
	// them; they go to the first port with link.
	first := true
	for _, m := range apPhyRE.FindAllStringSubmatch(string(secs.get("phy")), -1) {
		idx := int(atoi(m[1]))
		p := Port{Name: "lan" + strconv.Itoa(idx), MAC: s.MAC}
		if idx == 0 {
			p.Name = "wan"
		}
		if sp := apSpdRE.FindStringSubmatch(m[2]); sp != nil {
			p.Up, p.Speed, p.FullDuplex = true, int(atoi(sp[1])), sp[2] == "Full"
		}
		if p.Up && first {
			first = false
			c := bytesOf[short("bcmeth0")]
			p.RxBytes, p.TxBytes, p.BridgePort = c.in, c.out, brPort["bcmeth0"]
			k := pktsOf[short("bcmeth0")]
			p.RxPkts, p.TxPkts = k.in, k.out
		}
		s.Ports = append(s.Ports, p)
	}

	var radios []Radio
	idx := map[string]int{}
	for _, name := range secs.order {
		f := strings.Fields(name)
		if len(f) != 2 || f[0] != "iface" {
			continue
		}
		r, ok := parseAirPortRadio(f[1], secs)
		if !ok {
			continue // a VAP that is down or not on a channel
		}
		c := bytesOf[short(r.Iface)]
		k := pktsOf[short(r.Iface)]
		r.Stats = map[string]int64{"rx_bytes": c.in, "tx_bytes": c.out,
			"rx_packets": k.in, "tx_packets": k.out, "rx_errors": k.inErr, "tx_errors": k.outErr}
		i, seen := idx[r.Phy]
		if !seen {
			base := r
			base.SSID, base.BSSID, base.Stats, base.Stations = "", "", nil, nil
			radios = append(radios, base)
			i = len(radios) - 1
			idx[r.Phy] = i
		}
		radios[i].VAPs = append(radios[i].VAPs, r)
	}
	// AP VAPs before the client VAP, as on OpenWrt (phyN-ap0 sorts before
	// phyN-sta0): the controller takes a radio's width from its first VAP row,
	// and the uplink row carries none.
	for i := range radios {
		v := radios[i].VAPs
		sort.SliceStable(v, func(a, b int) bool { return v[a].Mode == "Master" && v[b].Mode != "Master" })
	}
	sort.SliceStable(radios, func(i, j int) bool { return radios[i].Band > radios[j].Band }) // ng before na
	s.Radios = radios
	return s, nil
}

func parseAirPortRadio(iface string, secs *secMap) (Radio, bool) {
	text := string(secs.get("iface " + iface))
	m := apIfRE.FindStringSubmatch(text)
	if m == nil || !strings.Contains(text, "RUNNING") {
		return Radio{}, false
	}
	freq := int(atoi(m[3]))
	r := Radio{Iface: iface, SSID: strings.Trim(m[1], `"`), Channel: int(atoi(m[2])), BSSID: strings.ToLower(m[5]),
		HWName: "Broadcom BCM4360", NSS: 3, Width: 20, Band: "na", Phy: "bwl0", VHT: true}
	if freq < 3000 {
		r.Band, r.Phy, r.VHT = "ng", "bwl1", false
	}
	r.Section = r.Phy
	for _, w := range []int{160, 80, 40} {
		if strings.HasSuffix(m[4], "/"+strconv.Itoa(w)) {
			r.Width = w
			break
		}
	}
	if t := apTxRE.FindStringSubmatch(text); t != nil {
		p, _ := strconv.ParseFloat(t[1], 64)
		r.TxPower = int(p)
		r.TxMax = r.TxPower
	}
	switch {
	case strings.Contains(text, "hostap"):
		r.Mode = "Master"
	case strings.Contains(text, "status: associated"):
		r.Mode = "Client"
	default:
		return Radio{}, false // a client VAP that is not associated
	}
	if strings.Contains(text, "authmode WPA") {
		r.Encryption = "psk"
	}
	r.Noise = int(atoi(strings.TrimSpace(string(secs.get("noise " + iface)))))

	// Signal per station from the net80211 table (RSSI column, in dBm here).
	rssi := map[string]int{}
	for _, line := range strings.Split(string(secs.get("sta "+iface)), "\n") {
		f := strings.Fields(line)
		if len(f) >= 6 && strings.Count(f[0], ":") == 5 {
			v, _ := strconv.ParseFloat(f[5], 64)
			rssi[strings.ToLower(f[0])] = int(v)
		}
	}
	for _, blk := range strings.Split(string(secs.get("assoc "+iface)), "--- ")[1:] {
		mac, body, _ := strings.Cut(blk, "\n")
		st := Station{MAC: strings.ToLower(strings.TrimSpace(mac)), Noise: r.Noise, MHz: r.Width}
		st.Signal = rssi[st.MAC]
		st.SignalAvg = st.Signal
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			k, v, ok := strings.Cut(line, ":")
			f := strings.Fields(line)
			switch {
			case len(f) == 3 && f[0] == "idle":
				st.Inactive = atoi(f[1]) * 1000
			case len(f) == 4 && f[0] == "in" && f[1] == "network":
				st.Connected = atoi(f[2])
			case strings.HasPrefix(line, "state:"):
				// an AP lists AUTHORIZED once the 4-way handshake is done; the
				// client side has no such flag for its own AP
				st.Authorized = strings.Contains(line, "AUTHORIZED") || r.Mode == "Client"
			case strings.HasPrefix(line, "flags "):
				st.HT, st.VHT = strings.Contains(line, "N_CAP"), strings.Contains(line, "VHT_CAP")
			case !ok:
			case k == "tx data pkts":
				st.TxPackets = atoi(strings.TrimSpace(v))
			case k == "tx data bytes":
				st.TxBytes = atoi(strings.TrimSpace(v))
			case k == "rx data pkts":
				st.RxPackets = atoi(strings.TrimSpace(v))
			case k == "rx data bytes":
				st.RxBytes = atoi(strings.TrimSpace(v))
			case k == "tx failures":
				st.TxFailed = atoi(strings.TrimSpace(v))
			case k == "rate of last tx pkt":
				st.TxRate = int(atoi(strings.TrimSuffix(strings.TrimSpace(v), " kbps")))
			case k == "rate of last rx pkt":
				st.RxRate = int(atoi(strings.TrimSuffix(strings.TrimSpace(v), " kbps")))
			}
		}
		if st.MAC != "" {
			r.Stations = append(r.Stations, st)
		}
	}
	return r, true
}

// normMAC pads a MAC printed without leading zeros (2:bb:0:0:0:1).
func normMAC(m string) string {
	p := strings.Split(strings.ToLower(m), ":")
	for i := range p {
		if len(p[i]) == 1 {
			p[i] = "0" + p[i]
		}
	}
	return strings.Join(p, ":")
}

// IsAirPort reports whether the AP is an AirPort base station (read-only).
func (cl *Client) IsAirPort() bool { return cl.t.Platform == PlatformAirPort }

// airportLED maps a UniFi LED state onto the base station's one status LED
// (ACP property LEDc: 1 = amber, 2 = green; it has no blue and no off):
// steady blue (adopted and connected) is green, everything else amber.
func (cl *Client) airportLED(s LEDState) error {
	v := 1
	if s == LEDBlue {
		v = 2
	}
	_, err := cl.run("/usr/bin/acp -q LEDc=" + strconv.Itoa(v))
	return err
}
