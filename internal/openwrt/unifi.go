// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"github.com/antoniocosta/unifi-openwrt/internal/models"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Fields returns the inform fields that describe the AP's radios, VAPs,
// clients, ports and system, in the shape a UniFi AP sends (docs/internals.md).
// prev (may be nil) is the previous snapshot, for channel-use deltas.
// wlanIDs maps an SSID to the controller's WLAN id (from system_cfg), so the
// controller can tie a VAP to its WLAN.
// Profile carries per-band radio capability words of the UniFi model we
// present (nil = none). Hardware facts (channel, power range, NSS, 11ac)
// still come from the AP itself.
type RadioCaps struct {
	IEEEModes, RadioCaps, RadioCaps2 int64
	HasDFS, HasFCCDFS                bool
}

// CfgInfo is what the controller's system_cfg says about VAPs: the WLAN id
// per SSID and the device name (athN) per SSID and radio.
type CfgInfo struct {
	IDs     map[string]string // ssid -> wlan id
	DevName map[string]string // ssid + "|" + radio name (wifiN) -> athN
	// GuestVAP: devnames of guest VAPs (aaa.N.is_guest). Authorized: guest
	// MACs the portal let in (filled by the daemon).
	GuestVAP   map[string]bool
	Authorized map[string]bool
	// PortVLAN: 1-based port_idx -> native VLAN (pvid) from switch.port.<idx>.pvid.
	PortVLAN map[int]int
}

func (s *Snapshot) Fields(prev *Snapshot, info CfgInfo, caps map[string]RadioCaps) map[string]any {
	m := map[string]any{
		"uptime":   s.Uptime,
		"ip":       s.IP,
		"netmask":  s.Netmask,
		"uplink":   "eth0",
		"has_eth1": false,
		"sys_stats": map[string]any{
			"loadavg_1": fmt.Sprintf("%.2f", s.Load[0]), "loadavg_5": fmt.Sprintf("%.2f", s.Load[1]),
			"loadavg_15": fmt.Sprintf("%.2f", s.Load[2]),
			"mem_total":  s.MemTotal, "mem_used": s.MemTotal - s.MemFree - s.MemBuf - s.MemCache,
			"mem_buffer": s.MemBuf,
		},
	}
	if s.Gateway != "" {
		m["gateway_ip"] = s.Gateway
	}
	// system-stats: what the controller graphs as CPU / Memory (strings, like a UAP)
	cpu := 0.0
	if prev != nil && s.CPUTotal > prev.CPUTotal {
		dt, di := s.CPUTotal-prev.CPUTotal, s.CPUIdle-prev.CPUIdle
		cpu = 100 * float64(dt-di) / float64(dt)
	}
	mem := 0.0
	if s.MemTotal > 0 {
		mem = 100 * float64(s.MemTotal-s.MemFree-s.MemBuf-s.MemCache) / float64(s.MemTotal)
	}
	m["system-stats"] = map[string]any{"cpu": fmt.Sprintf("%.1f", cpu), "mem": fmt.Sprintf("%.1f", mem), "uptime": fmt.Sprintf("%d", s.Uptime)}

	// The uplink port becomes eth0 (a U7PG2 reports a single eth interface).
	phys := s.physPorts()
	if p := s.uplinkPort(); p != nil {
		m["if_table"] = []map[string]any{{
			"name": "eth0", "mac": s.MAC, "ip": s.IP, "netmask": s.Netmask, "up": p.Up,
			"speed": p.Speed, "full_duplex": p.FullDuplex, "num_port": max(1, len(phys)),
			"rx_bytes": p.RxBytes, "tx_bytes": p.TxBytes, "rx_packets": p.RxPkts, "tx_packets": p.TxPkts,
			// The controller merges this row into the stored uplink
			// (XCfthFC.getUpdatedUplink), so after a mesh spell the old
			// wireless keys stay; the topology draws a mesh health bar while
			// uplink.signal is non-zero (docs/internals.md). Overwrite them.
			"signal": 0, "rssi": 0, "is_mesh_v3": false,
		}}
	}
	// port_table: the physical Ethernet ports, as a UAP with two ports
	// reports them (port 1 "Main" = the uplink port, then the rest); the
	// controller keeps port_idx/media/poe/speed_caps and shows the row as
	// port_stats (gliBtHTTrWRd, docs/internals.md). An in-wall model numbers
	// its front ports from 1 and the rear uplink jack last (RearUplink).
	if len(phys) > 0 {
		rear := s.RearUplink > 0 && s.uplinkPort() != nil
		var rows []map[string]any
		for i, p := range phys {
			idx := s.portIdx(i, rear)
			name := "Main"
			if i == 1 {
				name = "Secondary"
			} else if i > 1 {
				name = fmt.Sprintf("Port %d", i+1)
			}
			if rear && i == 0 {
				name = "PoE In + Data"
			} else if rear {
				name = fmt.Sprintf("Port %d", idx)
			}
			speed := p.Speed
			if !p.Up || speed < 0 {
				speed = 0
			}
			uplink := i == 0 && p.Up
			row := map[string]any{
				"port_idx": idx, "name": name, "media": "GE", "ifname": p.Name,
				"poe_caps": 0, "port_poe": false, "speed_caps": 1048623, // 10/100/1000, autoneg
				"enable": true, "autoneg": true, "op_mode": "switch",
				"up": p.Up, "speed": speed, "full_duplex": p.Up && p.FullDuplex,
				"is_uplink": uplink,
				"rx_bytes":  p.RxBytes, "tx_bytes": p.TxBytes, "rx_packets": p.RxPkts, "tx_packets": p.TxPkts,
			}
			// mac_table: the wired clients behind a downlink port. A model
			// with the ACCESS_POINT_SWITCH feature (U6-IW and the other
			// in-wall APs) gets them listed as wired clients on that port
			// (lTGxQKc, docs/internals.md).
			if !uplink && p.BridgePort > 0 {
				vlan := info.PortVLAN[idx]
				if vlan <= 0 {
					vlan = 1
				}
				row["mac_table"] = s.portMACs(p.BridgePort, vlan)
			}
			rows = append(rows, row)
		}
		if rear {
			sort.SliceStable(rows, func(a, b int) bool { return rows[a]["port_idx"].(int) < rows[b]["port_idx"].(int) })
		}
		m["port_table"] = rows
		m["has_eth1"] = len(phys) > 1
		// lldp_table: the neighbours lldpd hears, on the ports as numbered
		// above. A neighbour on anything else (a bridge, a VLAN) is left out.
		var lldp []map[string]any
		for _, n := range s.LLDP {
			for _, row := range rows {
				if row["ifname"] != n.Port {
					continue
				}
				e := map[string]any{"local_port_idx": row["port_idx"], "local_port_name": n.Port, "is_wired": true,
					"chassis_id": n.ChassisID, "port_id": n.PortID}
				if n.ChassisDes != "" {
					e["chassis_descr"] = n.ChassisDes
				}
				if n.PortDescr != "" {
					e["port_descr"] = n.PortDescr
				}
				lldp = append(lldp, e)
			}
		}
		if lldp != nil {
			m["lldp_table"] = lldp
		}
	}

	var radios, rstats, vaps []map[string]any
	var nsta, nextAth, allSat int
	for i, r := range s.Radios {
		name := fmt.Sprintf("wifi%d", i)
		ht := fmt.Sprintf("%d", r.Width)
		radios = append(radios, map[string]any{
			"name": name, "radio": r.Band, "channel": r.Channel, "ht": ht,
			"min_txpower": r.TxMin, "max_txpower": r.TxMax, "nss": r.NSS,
			"tx_power": r.TxPower, "tx_power_mode": "custom",
			"radio_caps": 0, "antenna_gain": 3, "builtin_antenna": true, "builtin_ant_gain": 3,
			"is_11ac": r.VHT, "is_11ax": r.HE,
		})
		if r.W160 {
			// the controller offers 160 MHz only to a radio row with
			// has_ht160 (DRUVu); is_11ac/is_11ax alone cap it at 80
			radios[len(radios)-1]["has_ht160"] = true
		}
		if c, ok := caps[r.Band]; ok {
			rr := radios[len(radios)-1]
			rr["ieee_modes"], rr["radio_caps"], rr["radio_caps2"] = c.IEEEModes, c.RadioCaps, c.RadioCaps2
			if r.Band == "na" {
				rr["has_dfs"], rr["has_fccdfs"] = c.HasDFS, c.HasFCCDFS
			}
		}
		cuTotal, cuRx, cuTx := 0, 0, 0
		if prev != nil {
			if pr := prev.radio(r); pr != nil {
				if d := r.Survey.Active - pr.Survey.Active; d > 0 {
					cuTotal = pct(r.Survey.Busy-pr.Survey.Busy, d)
					cuRx = pct(r.Survey.Rx-pr.Survey.Rx, d)
					cuTx = pct(r.Survey.Tx-pr.Survey.Tx, d)
				}
			}
		}
		// the controller takes channel utilization from radio_table[].athstats
		// (UAP inform parser), not from radio_table_stats
		// interference = busy time that isn't our own rx/tx (neighbours,
		// noise): the airtime graph stacks self tx + self rx + interference
		cuInterf := max(0, cuTotal-cuRx-cuTx)
		avail := min(100, 100+cuRx+cuTx-cuTotal) // airtime others leave (satisfaction.go)
		radios[len(radios)-1]["cu_interf"] = cuInterf
		radios[len(radios)-1]["athstats"] = map[string]any{
			"name": name, "cu_total": cuTotal, "cu_self_rx": cuRx, "cu_self_tx": cuTx, "cu_interf": cuInterf,
			"ast_txto": 0, "ast_cst": 0, "ast_be_xmit": 0,
		}
		rstats = append(rstats, map[string]any{
			"name": name, "radio": r.Band, "channel": r.Channel, "tx_power": r.TxPower,
			"num_sta": 0, "user-num_sta": 0, "guest-num_sta": 0,
			"cu_total": cuTotal, "cu_self_rx": cuRx, "cu_self_tx": cuTx, "cu_interf": cuInterf, "noise": r.Noise,
		})

		radioSta, radioSat := 0, 0
		for _, v := range r.VAPs {
			vname := info.DevName[v.SSID+"|"+name]
			if vname == "" {
				vname = fmt.Sprintf("ath%d", nextAth)
			}
			nextAth++
			if v.Mode == "Client" {
				// mesh child: this STA is the wireless uplink, not a user VAP.
				// Its SSID is the vwire one, whose devname is the downlink VAP's.
				vname = fmt.Sprintf("ath%d", nextAth-1)
				m["uplink"] = vname
				vaps = append(vaps, uplinkVAP(vname, name, r, v, s.Neighbours()))
				continue
			}
			guest := info.GuestVAP[vname]
			stas := []map[string]any{}
			satReal, satAnom := 0, 0 // sum of the clients' satisfaction_real, their anomaly bits
			for _, st := range v.Stations {
				attMov, retMov, dropMov := staMov(st, prev.station(st.MAC))
				row := map[string]any{
					"mac": st.MAC, "authorized": st.Authorized,
					"signal": st.Signal, "noise": st.Noise, "rssi": st.Signal - st.Noise,
					"tx_rate": st.TxRate, "rx_rate": st.RxRate,
					// AP's point of view: tx = to the client, rx = from it.
					"tx_bytes": st.TxBytes, "rx_bytes": st.RxBytes,
					"tx_packets": st.TxPackets, "rx_packets": st.RxPackets,
					"tx_retries": st.TxRetries,
					// as a UAP: the controller's retry percentage comes from
					// the *_mov fields (movement since the last inform); the
					// UI's Wi-Fi 4/5 and 1x1/2x2 from tx_mcs and nss, and the
					// controller derives radio_proto itself ("g" for a client
					// without HT, "ng" with). tx_power: the radio's, half dB.
					"wifi_tx_attempts": st.TxPackets + st.TxRetries, "wifi_tx_attempts_mov": attMov,
					"tx_retries_mov": retMov, "wifi_tx_dropped": st.TxFailed, "wifi_tx_dropped_mov": dropMov,
					"tx_retry_burst_count": 0, "tx_mcs": st.TxMCS, "tx_power": 2 * r.TxPower,
					"uptime": st.Connected, "idletime": st.Inactive / 1000,
					"is_11n": st.HT, "is_11ac": st.VHT, "is_11ax": st.HE,
					"channel": r.Channel, "radio": r.Band, "essid": v.SSID, "bssid": v.BSSID,
					"state": 15, "state_ht": st.HT, "state_pwrmgt": false,
					"is_guest": guest,
				}
				if guest {
					// portal state; WPA authorization stays in the AP
					row["authorized"] = info.Authorized[strings.ToLower(st.MAC)]
				}
				if ip, ok := s.ARP[st.MAC]; ok {
					row["ip"] = ip
				}
				if n := clientNSS(st.MAC, st.NSS, s.Time); n > 0 {
					row["nss"] = n
				}
				// chwidth: index into 20/40/80/160 MHz (controller: NRLJigwa,
				// enum HT_20=0 .. HT_160=3); without it the panel guesses 20
				if w, ok := map[int]int{20: 0, 40: 1, 80: 2, 160: 3}[st.MHz]; ok {
					row["chwidth"] = w
				}
				sr := staSatisfaction(st, r.Band == "na", avail, s.Time)
				row["satisfaction"], row["satisfaction_real"], row["satisfaction_now"] = sr.Reported, sr.Real, sr.Now
				row["anomalies"] = sr.Anomalies
				satReal, satAnom = satReal+sr.Real, satAnom|sr.Anomalies
				stas = append(stas, row)
			}
			radioSta += len(v.Stations)
			// per-VAP Wi-Fi counters (the device panel's Tx Retry/Dropped):
			// summed over the associated clients (iwinfo assoclist)
			var retries, failed, txPk int64
			sig := 0
			for _, st := range v.Stations {
				retries += st.TxRetries
				failed += st.TxFailed
				txPk += st.TxPackets
				sig += st.Signal
			}
			vap := map[string]any{
				"name": vname, "radio_name": name, "radio": r.Band,
				"essid": v.SSID, "bssid": v.BSSID, "channel": r.Channel, "tx_power": r.TxPower,
				"state": "RUN", "up": true, "usage": "user", "is_guest": guest,
				"num_sta": len(v.Stations), "sta_table": stas,
				"rx_bytes": v.Stats["rx_bytes"], "tx_bytes": v.Stats["tx_bytes"],
				"rx_packets": v.Stats["rx_packets"], "tx_packets": v.Stats["tx_packets"],
				"rx_errors": v.Stats["rx_errors"], "tx_errors": v.Stats["tx_errors"],
				"rx_dropped": v.Stats["rx_dropped"], "tx_dropped": v.Stats["tx_dropped"],
				"ccq": 1000,
				"bw":  r.Width, "extchannel": 0,
				"tx_retries": retries, "tx_combined_retries": retries,
				"wifi_tx_attempts": txPk + retries, "wifi_tx_dropped": failed,
				"tx_total": txPk, "tx_success": max(txPk-failed, 0),
			}
			if n := len(v.Stations); n > 0 {
				vap["avg_client_signal"] = sig / n
				// VAP = mean of its clients; radio and AP = mean over their clients
				vap["satisfaction"], vap["satisfaction_real"] = satClamp(satReal/n), min(100, satReal/n)
				vap["anomalies"] = satAnom
				radioSat, allSat = radioSat+satReal, allSat+satReal
			}
			if id, ok := info.IDs[v.SSID]; ok {
				vap["id"] = id
			}
			vaps = append(vaps, vap)
		}
		nsta += radioSta
		rs := rstats[len(rstats)-1]
		rs["num_sta"], rs["user-num_sta"] = radioSta, radioSta
		if radioSta > 0 {
			radios[len(radios)-1]["athstats"].(map[string]any)["satisfaction"] = satClamp(radioSat / radioSta)
			rs["satisfaction"] = satClamp(radioSat / radioSta)
		}
	}
	if up, _ := m["uplink"].(string); up != "eth0" {
		if it, ok := m["if_table"].([]map[string]any); ok && len(it) > 0 {
			it[0]["up"] = false // no cable: the uplink is wireless
		}
	}
	if nsta > 0 {
		m["satisfaction"] = satClamp(allSat / nsta)
	}
	satPrune(s.Time)
	nssPrune(s.Time)
	m["radio_table"] = radios
	m["radio_table_stats"] = rstats
	m["vap_table"] = vaps
	m["num_sta"] = nsta
	m["user-num_sta"] = nsta
	m["stat"] = map[string]any{"user-num_sta": nsta}
	return m
}

// staMov: a client's tx attempts (packets + retries), retries and drops
// since the previous reading; zeros with no previous reading or when the
// counters went backwards (a re-association restarts them).
func staMov(st Station, prev *Station) (attempts, retries, dropped int64) {
	if prev == nil || st.TxPackets < prev.TxPackets || st.TxRetries < prev.TxRetries || st.TxFailed < prev.TxFailed {
		return 0, 0, 0
	}
	retries = st.TxRetries - prev.TxRetries
	return st.TxPackets - prev.TxPackets + retries, retries, st.TxFailed - prev.TxFailed
}

// A client's spatial streams show only in the rates of data frames (a
// keep-alive at a legacy rate has none): the most seen per client, kept
// until it has been gone for 10 minutes.
type nssSeen struct {
	nss  int
	last time.Time
}

var (
	nssMu  sync.Mutex
	nssMem = map[string]nssSeen{}
)

// clientNSS records a reading of a client's streams (0 = none) and
// returns the most seen, 0 = not known yet.
func clientNSS(mac string, nss int, now time.Time) int {
	nssMu.Lock()
	defer nssMu.Unlock()
	n := max(nss, nssMem[mac].nss)
	if n > 0 {
		nssMem[mac] = nssSeen{n, now}
	}
	return n
}

func nssPrune(now time.Time) {
	nssMu.Lock()
	defer nssMu.Unlock()
	for m, e := range nssMem {
		if now.Sub(e.last) > 10*time.Minute {
			delete(nssMem, m)
		}
	}
}

// station: the client with this MAC on any VAP (nil if none, or s is nil).
func (s *Snapshot) station(mac string) *Station {
	if s == nil {
		return nil
	}
	for _, r := range s.Radios {
		for _, v := range r.VAPs {
			for i := range v.Stations {
				if v.Stations[i].MAC == mac {
					return &v.Stations[i]
				}
			}
		}
	}
	return nil
}

// radio is the same radio in another snapshot: by wifi-device section when
// it has one (a multi-radio wiphy has several radios on one phy), else by phy.
func (s *Snapshot) radio(r Radio) *Radio {
	for i := range s.Radios {
		if r.Section != "" && s.Radios[i].Section == r.Section {
			return &s.Radios[i]
		}
	}
	if r.Section != "" || r.Phy == "" {
		return nil // not there before, or a wifi-device with no interface up
	}
	for i := range s.Radios {
		if s.Radios[i].Phy == r.Phy {
			return &s.Radios[i]
		}
	}
	return nil
}

// uplinkPort: the port named by UplinkName; else the wired port the
// gateway is behind; failing that, the wired port that is up (prefer a "lan"
// port, as on the OnHub where the cable is in its LAN port).
func (s *Snapshot) uplinkPort() *Port {
	for i := range s.Ports {
		if s.UplinkName != "" && s.Ports[i].Name == s.UplinkName {
			return &s.Ports[i]
		}
	}
	if p := s.gatewayPort(); p != nil {
		return p
	}
	var best *Port
	for i := range s.Ports {
		p := &s.Ports[i]
		if !p.Up {
			continue
		}
		if best == nil || strings.HasPrefix(p.Name, "lan") {
			best = p
		}
	}
	return best
}

// gatewayPort: the up port whose br-lan FDB entry holds the gateway's MAC
// (nil when the gateway is not in the ARP table or not behind a wired port).
func (s *Snapshot) gatewayPort() *Port {
	if s.Gateway == "" {
		return nil
	}
	for mac, ip := range s.ARP {
		if ip != s.Gateway {
			continue
		}
		for _, e := range s.FDB {
			if e.MAC != mac || e.Local {
				continue
			}
			for i := range s.Ports {
				if p := &s.Ports[i]; p.Up && p.BridgePort == e.Port {
					return p
				}
			}
		}
	}
	return nil
}

// portMACs: the mac_table rows of one br-lan port, one per MAC (the
// youngest entry when a MAC shows up on more than one VLAN). The brforward
// file carries no VLAN, so each row carries the port's native VLAN (1 when
// switch port VLANs are not configured).
func (s *Snapshot) portMACs(port, vlan int) []map[string]any {
	age := map[string]int64{}
	var order []string
	for _, e := range s.FDB {
		if e.Port != port || e.Local || e.MAC == s.MAC {
			continue
		}
		if a, ok := age[e.MAC]; !ok {
			order = append(order, e.MAC)
		} else if a <= e.Age {
			continue
		}
		age[e.MAC] = e.Age
	}
	rows := []map[string]any{}
	for _, mac := range order {
		rows = append(rows, map[string]any{"mac": mac, "age": age[mac], "vlan": vlan, "static": false})
	}
	return rows
}

// physPorts: the physical Ethernet ports, the uplink port first. Virtual
// devices (no MAC: teql0 and the like) are left out.
func (s *Snapshot) physPorts() []Port {
	up := s.uplinkPort()
	var out []Port
	if up != nil {
		out = append(out, *up)
	}
	for i := range s.Ports {
		p := &s.Ports[i]
		if p == up || p.MAC == "" || strings.HasPrefix(p.Name, "teql") {
			continue
		}
		out = append(out, *p)
	}
	return out
}

func pct(a, b int64) int {
	if b <= 0 || a < 0 {
		return 0
	}
	v := int((a*200 + b) / (2 * b)) // rounded: 0.8 % own airtime is 1 %, not 0
	if v > 100 {
		v = 100
	}
	return v
}

// InfoFromCfg reads WLAN ids and VAP device names from a system_cfg text.
func InfoFromCfg(systemCfg string) CfgInfo {
	info := CfgInfo{IDs: WLANIDs(systemCfg), DevName: map[string]string{}, GuestVAP: map[string]bool{}}
	f := map[string]map[string]string{}
	for _, line := range strings.Split(systemCfg, "\n") {
		k, v, ok := strings.Cut(line, "=")
		parts := strings.Split(k, ".")
		if !ok || len(parts) != 3 || parts[0] != "wireless" {
			continue
		}
		if f[parts[1]] == nil {
			f[parts[1]] = map[string]string{}
		}
		f[parts[1]][parts[2]] = v
	}
	for _, w := range f {
		if w["ssid"] != "" && w["parent"] != "" && w["devname"] != "" {
			info.DevName[w["ssid"]+"|"+w["parent"]] = w["devname"]
		}
	}
	cfg := translate.ParseCfg(systemCfg)
	for _, d := range translate.GuestPolicy(cfg).Devnames {
		info.GuestVAP[d] = true
	}
	if cfg["switch.status"] == "enabled" && cfg["switch.vlan.status"] == "enabled" {
		info.PortVLAN = map[int]int{}
		for k, v := range cfg {
			if !strings.HasPrefix(k, "switch.port.") || !strings.HasSuffix(k, ".pvid") {
				continue
			}
			mid := strings.TrimSuffix(strings.TrimPrefix(k, "switch.port."), ".pvid")
			idx, err1 := strconv.Atoi(mid)
			vid, err2 := strconv.Atoi(v)
			if err1 == nil && err2 == nil &&
				idx > 0 && strconv.Itoa(idx) == mid &&
				vid >= 1 && vid <= 4094 && strconv.Itoa(vid) == v {
				info.PortVLAN[idx] = vid
			}
		}
	}
	return info
}

// WLANIDs reads ssid -> wlan id from a system_cfg text (wireless.N.ssid and
// wireless.N.id lines). Values other than ids and ssids are not read.
func WLANIDs(systemCfg string) map[string]string {
	ssid := map[string]string{}
	id := map[string]string{}
	for _, line := range strings.Split(systemCfg, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(k, "wireless.") {
			continue
		}
		parts := strings.Split(k, ".")
		if len(parts) != 3 {
			continue
		}
		switch parts[2] {
		case "ssid":
			ssid[parts[1]] = v
		case "id":
			id[parts[1]] = v
		}
	}
	out := map[string]string{}
	for n, s := range ssid {
		if i, ok := id[n]; ok && s != "" {
			out[s] = i
		}
	}
	return out
}

// RadioNames maps the radio names we report (wifiN, by position) to the UCI
// band of that radio ("2g"/"5g").
func (s *Snapshot) RadioNames() map[string]string {
	m := map[string]string{}
	for i, r := range s.Radios {
		b := "5g"
		switch r.Band {
		case "ng":
			b = "2g"
		case "6e":
			b = "6g"
		}
		m[fmt.Sprintf("wifi%d", i)] = b
	}
	return m
}

// RadioSections maps the radio names we report (wifiN) to their UCI
// wifi-device, for radios whose device is known.
func (s *Snapshot) RadioSections() map[string]string {
	m := map[string]string{}
	for i, r := range s.Radios {
		if r.Section != "" {
			m[fmt.Sprintf("wifi%d", i)] = r.Section
		}
	}
	return m
}

// RadioTxPower maps the radio names we report (wifiN) to the {min, max} dBm
// the hardware offers, for radios where it is known.
func (s *Snapshot) RadioTxPower() map[string][2]int {
	m := map[string][2]int{}
	for i, r := range s.Radios {
		if r.TxMax > 0 {
			m[fmt.Sprintf("wifi%d", i)] = [2]int{r.TxMin, r.TxMax}
		}
	}
	return m
}

// portIdx maps an index in physPorts() to its UniFi port_idx.
func (s *Snapshot) portIdx(i int, rear bool) int {
	if rear {
		if i == 0 {
			return s.RearUplink
		}
		if i >= s.RearUplink {
			return i + 1
		}
		return i
	}
	return i + 1
}

// UplinkPortName returns the OpenWrt netdev name of the resolved uplink port,
// or "" when no uplink port is known.
func (s *Snapshot) UplinkPortName() string {
	if p := s.uplinkPort(); p != nil {
		return p.Name
	}
	return ""
}

// DownlinkPorts maps UniFi port_idx -> OpenWrt netdev name for non-uplink
// physical Ethernet ports, using the same numbering as port_table.
func (s *Snapshot) DownlinkPorts() map[int]string {
	phys := s.physPorts()
	if len(phys) == 0 {
		return nil
	}
	up := s.uplinkPort()
	rear := s.RearUplink > 0 && up != nil
	out := map[int]string{}
	for i, p := range phys {
		if i == 0 && up != nil {
			continue // uplink port
		}
		out[s.portIdx(i, rear)] = p.Name
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Describe summarises the AP for models.Suggest.
func (s *Snapshot) Describe() models.AP {
	var d models.AP
	seen := map[string]bool{}
	for _, r := range s.Radios {
		b := r.Band
		if !seen[b] {
			seen[b] = true
			d.Bands = append(d.Bands, b)
		}
		d.NSS = max(d.NSS, r.NSS)
		g := 4
		if r.VHT {
			g = 5
		}
		if r.HE {
			g = 6
		}
		d.Gen = max(d.Gen, g)
	}
	return d
}

// uplinkVAP is the vap_table row of a wireless (mesh) uplink: the STA's own
// BSSID, the parent's BSSID as ap_mac, and the link figures from the parent's
// row in the STA's assoclist (rates in kbit/s).
func uplinkVAP(vname, radioName string, r, v Radio, neigh map[string]string) map[string]any {
	row := map[string]any{
		"name": vname, "radio_name": radioName, "radio": r.Band,
		"essid": v.SSID, "bssid": v.BSSID, "channel": r.Channel, "tx_power": r.TxPower,
		"state": "RUN", "up": true, "usage": "uplink", "is_guest": false, "is_wds": true,
		"num_sta": 0, "sta_table": []map[string]any{},
		"rx_bytes": v.Stats["rx_bytes"], "tx_bytes": v.Stats["tx_bytes"],
		"rx_packets": v.Stats["rx_packets"], "tx_packets": v.Stats["tx_packets"],
	}
	if len(v.Stations) > 0 {
		// The controller resolves the uplink from this VAP's sta_table: the
		// parent's vwire BSSID, matched against its vwire_vap_table.
		p := v.Stations[0]
		row["sta_table"] = []map[string]any{{
			"mac": p.MAC, "ap_mac": p.MAC, "bssid": v.BSSID, "essid": v.SSID,
			"signal": p.Signal, "noise": p.Noise, "rssi": p.Signal - p.Noise,
			"tx_rate": p.TxRate, "rx_rate": p.RxRate,
			"tx_bytes": p.TxBytes, "rx_bytes": p.RxBytes, "tx_packets": p.TxPackets, "rx_packets": p.RxPackets,
			"uptime": p.Connected, "idletime": p.Inactive / 1000,
			"channel": r.Channel, "radio": r.Band, "is_11ac": p.VHT, "is_11n": p.HT, "authorized": true, "state": 15,
		}}
		// serialno = the parent's device MAC: the controller resolves the
		// uplink device from it (a site mesh ESSID doesn't carry one).
		if dev := parentDevice(p.MAC, neigh); dev != "" {
			row["sta_table"].([]map[string]any)[0]["serialno"] = dev
		}
		row["num_sta"] = 1
		row["ap_mac"] = p.MAC
		row["signal"], row["rssi"], row["noise"] = p.Signal, p.Signal-p.Noise, p.Noise
		row["tx_rate"], row["rx_rate"] = p.TxRate, p.RxRate
		row["uptime"] = p.Connected
	} else {
		row["up"], row["state"] = false, "INIT"
	}
	return row
}

// Neighbours: the MACs the AP knows on its LAN, from the ARP table and from
// the br-lan forwarding database. A mesh child never talks IP to its parent
// AP, so the parent's own MAC is often only in the forwarding database.
func (s *Snapshot) Neighbours() map[string]string {
	out := make(map[string]string, len(s.ARP)+len(s.FDB))
	for _, e := range s.FDB {
		if !e.Local {
			out[strings.ToLower(e.MAC)] = ""
		}
	}
	for mac, ip := range s.ARP {
		out[strings.ToLower(mac)] = ip
	}
	return out
}

// bssidOfDevice reports whether bssid can be a BSSID of the device with MAC
// mac. A device derives its BSSIDs from its MAC: b[0] is free (locally
// administered and index bits), b[1] and b[2] are the MAC's, and of b[3],
// b[4], b[5] at most one differs, raised by 1-15 (mod 256). As a
// controller's vap_table lists them (addresses made up, patterns kept):
//
//	UAP-AC-Pro 48:a1:01:13:27:5c: 48:a1:01:14:27:5c, 52:a1:01:14:27:5c (b[3])
//	U7 Lite    70:e5:f6:47:9c:3a: 76:e5:f6:47:9c:3b, 7a:e5:f6:47:9c:3b (b[5])
//	OpenWrt    08:7a:6b:5c:4d:e1: 08:7a:6b:5c:4d:e2, 0a:7a:6b:5c:4d:e3 (b[5])
func bssidOfDevice(bssid, mac string) bool {
	b, err1 := net.ParseMAC(bssid)
	m, err2 := net.ParseMAC(mac)
	if err1 != nil || err2 != nil || len(b) != 6 || len(m) != 6 || b[1] != m[1] || b[2] != m[2] {
		return false
	}
	raised := 0
	for i := 3; i < 6; i++ {
		if d := b[i] - m[i]; d > 15 {
			return false
		} else if d > 0 {
			raised++
		}
	}
	return raised <= 1
}

// parentDevice finds the device MAC a BSSID belongs to among the LAN
// devices (bssidOfDevice). Exactly one match, or nothing.
func parentDevice(bssid string, neigh map[string]string) string {
	b := strings.ToLower(bssid)
	found := ""
	for mac := range neigh {
		m := strings.ToLower(mac)
		if m != b && bssidOfDevice(b, m) {
			if found != "" && found != m {
				return ""
			}
			found = m
		}
	}
	return found
}
