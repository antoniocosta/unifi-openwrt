// SPDX-License-Identifier: GPL-2.0-or-later

// Package openwrt reads an OpenWrt access point over SSH (ubus, iwinfo, iw,
// /proc, /sys) and turns it into the radio, VAP, station and system fields of
// a UniFi AP inform. Nothing here is specific to one device: radios and
// interfaces are discovered.
//
// Read-only: this package never changes the AP.
package openwrt

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math/bits"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Target is how to reach the AP.
type Target struct {
	Addr       string // host:port, e.g. 192.168.1.2:22, or "local" (run on this machine: the daemon on the AP itself)
	User       string
	KeyFile    string // private key (the daemon's own)
	KnownHosts string // known_hosts file with the AP's host key
}

type Client struct {
	t  Target
	mu sync.Mutex
	c  *ssh.Client
}

func NewClient(t Target) *Client { return &Client{t: t} }

func (cl *Client) conn() (*ssh.Client, error) {
	if cl.c != nil {
		return cl.c, nil
	}
	key, err := os.ReadFile(cl.t.KeyFile)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, err
	}
	hk, err := knownhosts.New(cl.t.KnownHosts)
	if err != nil {
		return nil, err
	}
	c, err := ssh.Dial("tcp", cl.t.Addr, &ssh.ClientConfig{
		User: cl.t.User, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hk, Timeout: 8 * time.Second,
		// Match the key type recorded in known_hosts (OpenSSH stored ed25519).
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
	})
	if err != nil {
		return nil, err
	}
	cl.c = c
	return c, nil
}

// Run runs one command and returns stdout. The connection is reused and
// re-dialled after an error.
func (cl *Client) Run(cmd string) ([]byte, error) {
	if cl.t.Addr == "local" {
		return localRun(cmd, nil)
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		c, err := cl.conn()
		if err != nil {
			return nil, err
		}
		s, err := c.NewSession()
		if err != nil {
			c.Close()
			cl.c = nil
			continue
		}
		var out, errb bytes.Buffer
		s.Stdout, s.Stderr = &out, &errb
		err = s.Run(cmd)
		s.Close()
		if err != nil {
			return out.Bytes(), fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
		}
		return out.Bytes(), nil
	}
	return nil, fmt.Errorf("ssh: could not open a session")
}

func (cl *Client) Close() {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.c != nil {
		cl.c.Close()
		cl.c = nil
	}
}

// collectScript prints sections "=== name" followed by their content.
const collectScript = `
echo "=== board"; ubus call system board
echo "=== info"; ubus call system info
echo "=== route"; ip route show default
echo "=== cpu"; head -1 /proc/stat
echo "=== arp"; cat /proc/net/arp
for i in $(ubus call iwinfo devices | jsonfilter -e '@.devices[*]'); do
  case $i in uowscan*) continue;; esac
  p=$(cat /sys/class/net/$i/phy80211/name 2>/dev/null)
  echo "=== iface $i $p"; ubus call iwinfo info "{\"device\":\"$i\"}"
  echo "=== assoc $i"; ubus call iwinfo assoclist "{\"device\":\"$i\"}"
  echo "=== txpw $i"; ubus call iwinfo txpowerlist "{\"device\":\"$i\"}"
  echo "=== ant $i"; iw phy $p info | grep 'Available Antennas'
  echo "=== survey $i"; iw dev $i survey dump | grep -A6 'in use'
  echo "=== stats $i"; for f in rx_bytes tx_bytes rx_packets tx_packets rx_errors tx_errors rx_dropped tx_dropped; do printf '%s=%s\n' $f $(cat /sys/class/net/$i/statistics/$f); done
done
for e in $(ls /sys/class/net); do
  [ -e /sys/class/net/$e/phy80211 ] && continue
  case $e in lo|br-*|eth*) continue;; esac
  echo "=== port $e"; printf 'mac=%s\nspeed=%s\nduplex=%s\noper=%s\n' $(cat /sys/class/net/$e/address) $(cat /sys/class/net/$e/speed 2>/dev/null || echo 0) $(cat /sys/class/net/$e/duplex 2>/dev/null || echo unknown) $(cat /sys/class/net/$e/operstate)
  for f in rx_bytes tx_bytes rx_packets tx_packets; do printf '%s=%s\n' $f $(cat /sys/class/net/$e/statistics/$f); done
done
echo "=== lan"; ip -4 addr show br-lan; cat /sys/class/net/br-lan/address
`

// Snapshot is one reading of the AP.
type Snapshot struct {
	Time     time.Time
	Model    string
	Hostname string
	Release  string
	Uptime   int64
	Load     [3]float64
	CPUTotal int64 // /proc/stat jiffies, all states
	CPUIdle  int64 // idle + iowait
	MemTotal int64
	MemFree  int64
	MemBuf   int64
	MemCache int64
	MAC      string // br-lan MAC (the AP's identity)
	IP       string
	Netmask  string
	Gateway  string
	Radios   []Radio
	Ports    []Port
	ARP      map[string]string // mac -> ip
}

type Radio struct {
	Iface, Phy  string
	Mode        string // iwinfo mode: "Master" (AP), "Client" (STA: mesh uplink), ...
	Band        string // "ng" (2.4 GHz) or "na" (5 GHz)
	SSID, BSSID string
	Channel     int
	Width       int // MHz
	TxPower     int
	TxMin       int
	TxMax       int
	Noise       int
	NSS         int
	HWName      string
	Encryption  string
	VHT, HE     bool // hardware supports 802.11ac / ax
	Survey      Survey
	Stats       map[string]int64
	Stations    []Station
	// VAPs: on a phy-level Radio, one entry per AP interface on that radio
	// (each carries its own SSID, BSSID, Stats, Stations).
	VAPs []Radio
}

type Survey struct {
	Active, Busy, Rx, Tx int64 // ms, cumulative since boot
}

type Station struct {
	MAC                  string
	Signal, SignalAvg    int
	Noise                int
	Inactive             int64 // ms
	Connected            int64 // s
	Authorized           bool
	RxBytes, TxBytes     int64
	RxPackets, TxPackets int64
	RxRate, TxRate       int // kbit/s
	TxRetries, TxFailed  int64
	HT, VHT, HE          bool
	MHz                  int
}

type Port struct {
	Name, MAC        string
	Speed            int
	FullDuplex, Up   bool
	RxBytes, TxBytes int64
	RxPkts, TxPkts   int64
}

// Collect reads the AP once.
func (cl *Client) Collect() (*Snapshot, error) {
	out, err := cl.Run(collectScript)
	if err != nil {
		return nil, err
	}
	return Parse(out, time.Now())
}

// Parse turns collectScript output into a Snapshot (separate for tests).
func Parse(out []byte, now time.Time) (*Snapshot, error) {
	secs := sections(out)
	s := &Snapshot{Time: now, ARP: map[string]string{}}

	var board struct {
		Hostname string                       `json:"hostname"`
		Model    string                       `json:"model"`
		Release  struct{ Description string } `json:"release"`
	}
	if err := json.Unmarshal(secs.get("board"), &board); err != nil {
		return nil, fmt.Errorf("board: %w", err)
	}
	s.Model, s.Hostname, s.Release = board.Model, board.Hostname, board.Release.Description

	var info struct {
		Uptime int64    `json:"uptime"`
		Load   [3]int64 `json:"load"`
		Memory struct {
			Total, Free, Buffered, Cached int64
		} `json:"memory"`
	}
	if err := json.Unmarshal(secs.get("info"), &info); err == nil {
		s.Uptime = info.Uptime
		for i, l := range info.Load {
			s.Load[i] = float64(l) / 65536
		}
		s.MemTotal, s.MemFree, s.MemBuf, s.MemCache = info.Memory.Total, info.Memory.Free, info.Memory.Buffered, info.Memory.Cached
	}
	if f := strings.Fields(string(secs.get("cpu"))); len(f) >= 5 && f[0] == "cpu" {
		for i, v := range f[1:] {
			n := atoi(v)
			s.CPUTotal += n
			if i == 3 || i == 4 { // idle, iowait
				s.CPUIdle += n
			}
		}
	}
	if f := strings.Fields(string(secs.get("route"))); len(f) >= 3 && f[0] == "default" {
		s.Gateway = f[2]
	}
	for _, line := range strings.Split(string(secs.get("arp")), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 4 && f[3] != "00:00:00:00:00:00" {
			s.ARP[strings.ToLower(f[3])] = f[0]
		}
	}
	lan := string(secs.get("lan"))
	for _, line := range strings.Split(lan, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "inet" {
			if ip, n, err := net.ParseCIDR(f[1]); err == nil {
				s.IP = ip.String()
				s.Netmask = net.IP(n.Mask).String()
			}
		}
		if len(f) == 1 && strings.Count(f[0], ":") == 5 {
			s.MAC = strings.ToLower(f[0])
		}
	}

	for _, name := range secs.order {
		f := strings.Fields(name)
		switch f[0] {
		case "iface":
			if len(f) < 3 {
				continue
			}
			r, err := parseRadio(f[1], f[2], secs)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f[1], err)
			}
			s.Radios = append(s.Radios, r)
		case "port":
			kv := keyvals(secs.get(name))
			p := Port{Name: f[1], MAC: kv["mac"], Up: kv["oper"] == "up", FullDuplex: kv["duplex"] == "full"}
			p.Speed = int(atoi(kv["speed"]))
			p.RxBytes, p.TxBytes = atoi(kv["rx_bytes"]), atoi(kv["tx_bytes"])
			p.RxPkts, p.TxPkts = atoi(kv["rx_packets"]), atoi(kv["tx_packets"])
			s.Ports = append(s.Ports, p)
		}
	}
	// Group interfaces by phy: one radio per phy, its interfaces as VAPs.
	var radios []Radio
	idx := map[string]int{}
	for _, r := range s.Radios {
		i, ok := idx[r.Phy]
		if !ok {
			base := r
			base.SSID, base.BSSID, base.Stats, base.Stations = "", "", nil, nil
			radios = append(radios, base)
			i = len(radios) - 1
			idx[r.Phy] = i
		}
		radios[i].VAPs = append(radios[i].VAPs, r)
	}
	for i := range radios {
		sort.Slice(radios[i].VAPs, func(a, b int) bool { return radios[i].VAPs[a].Iface < radios[i].VAPs[b].Iface })
	}
	s.Radios = radios
	sort.SliceStable(s.Radios, func(i, j int) bool { return s.Radios[i].Band > s.Radios[j].Band }) // ng before na
	return s, nil
}

func parseRadio(iface, phy string, secs *secMap) (Radio, error) {
	r := Radio{Iface: iface, Phy: phy}
	var info struct {
		SSID, BSSID string
		Mode        string                `json:"mode"`
		Channel     int                   `json:"channel"`
		Frequency   int                   `json:"frequency"`
		TxPower     int                   `json:"txpower"`
		Noise       int                   `json:"noise"`
		HTMode      string                `json:"htmode"`
		HWModes     []string              `json:"hwmodes"`
		Hardware    struct{ Name string } `json:"hardware"`
		Encryption  struct {
			WPA            []int    `json:"wpa"`
			Authentication []string `json:"authentication"`
		} `json:"encryption"`
	}
	if err := json.Unmarshal(secs.get("iface "+iface+" "+phy), &info); err != nil {
		return r, err
	}
	r.SSID, r.BSSID, r.Mode = info.SSID, strings.ToLower(info.BSSID), info.Mode
	r.Channel, r.TxPower, r.Noise, r.HWName = info.Channel, info.TxPower, info.Noise, info.Hardware.Name
	r.Band = "na"
	switch {
	case info.Frequency < 3000:
		r.Band = "ng"
	case info.Frequency > 5925:
		r.Band = "6e" // 6 GHz (UniFi's name for the band)
	}
	r.Width = 20
	for _, w := range []int{160, 80, 40} {
		if strings.HasSuffix(info.HTMode, strconv.Itoa(w)) {
			r.Width = w
			break
		}
	}
	r.Encryption = strings.Join(info.Encryption.Authentication, "+")
	for _, m := range info.HWModes {
		r.VHT = r.VHT || m == "ac"
		r.HE = r.HE || m == "ax"
	}

	var tp struct {
		Results []struct{ Dbm int } `json:"results"`
	}
	if json.Unmarshal(secs.get("txpw "+iface), &tp) == nil && len(tp.Results) > 0 {
		r.TxMin, r.TxMax = tp.Results[0].Dbm, tp.Results[len(tp.Results)-1].Dbm
	}
	r.NSS = 1
	if f := strings.Fields(string(secs.get("ant " + iface))); len(f) >= 4 {
		if m, err := strconv.ParseUint(strings.TrimPrefix(f[3], "0x"), 16, 32); err == nil && m > 0 {
			r.NSS = bits.OnesCount64(m)
		}
	}
	for _, line := range strings.Split(string(secs.get("survey "+iface)), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n := atoi(strings.TrimSuffix(strings.TrimSpace(v), " ms"))
		switch strings.TrimSpace(k) {
		case "channel active time":
			r.Survey.Active = n
		case "channel busy time":
			r.Survey.Busy = n
		case "channel receive time":
			r.Survey.Rx = n
		case "channel transmit time":
			r.Survey.Tx = n
		}
	}
	r.Stats = map[string]int64{}
	for k, v := range keyvals(secs.get("stats " + iface)) {
		r.Stats[k] = atoi(v)
	}

	var al struct {
		Results []struct {
			MAC        string `json:"mac"`
			Signal     int    `json:"signal"`
			SignalAvg  int    `json:"signal_avg"`
			Noise      int    `json:"noise"`
			Inactive   int64  `json:"inactive"`
			Connected  int64  `json:"connected_time"`
			Authorized bool   `json:"authorized"`
			Rx, Tx     struct {
				Packets, Bytes  int64
				Rate, MHz       int
				HT, VHT, HE     bool
				Retries, Failed int64
			}
		} `json:"results"`
	}
	if err := json.Unmarshal(secs.get("assoc "+iface), &al); err != nil {
		return r, fmt.Errorf("assoclist: %w", err)
	}
	for _, a := range al.Results {
		r.Stations = append(r.Stations, Station{
			MAC: strings.ToLower(a.MAC), Signal: a.Signal, SignalAvg: a.SignalAvg, Noise: a.Noise,
			Inactive: a.Inactive, Connected: a.Connected, Authorized: a.Authorized,
			RxBytes: a.Rx.Bytes, TxBytes: a.Tx.Bytes, RxPackets: a.Rx.Packets, TxPackets: a.Tx.Packets,
			RxRate: a.Rx.Rate, TxRate: a.Tx.Rate, TxRetries: a.Tx.Retries, TxFailed: a.Tx.Failed,
			HT: a.Rx.HT || a.Tx.HT, VHT: a.Rx.VHT || a.Tx.VHT, HE: a.Rx.HE || a.Tx.HE, MHz: max(a.Rx.MHz, a.Tx.MHz), // rx alone is 20 after a legacy-rate keep-alive
		})
	}
	return r, nil
}

type secMap struct {
	order []string
	data  map[string][]byte
}

func (m *secMap) get(name string) []byte { return m.data[name] }

func sections(out []byte) *secMap {
	m := &secMap{data: map[string][]byte{}}
	var cur string
	var buf bytes.Buffer
	flush := func() {
		if cur != "" {
			m.data[cur] = append([]byte(nil), buf.Bytes()...)
			m.order = append(m.order, cur)
		}
		buf.Reset()
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if name, ok := strings.CutPrefix(line, "=== "); ok {
			flush()
			cur = name
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return m
}

func keyvals(b []byte) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return kv
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

// CollectScript returns the read-only shell script Collect runs.
func CollectScript() string { return collectScript }

// localRun runs a command with sh on this machine (Target.Addr "local").
func localRun(cmd string, stdin []byte) ([]byte, error) {
	c := exec.Command("/bin/sh", "-c", cmd)
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	if err := c.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}
