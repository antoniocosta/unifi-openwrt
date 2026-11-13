// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"github.com/antoniocosta/unifi-openwrt/internal/models"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
	"strings"
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
	// port_stats (gliBtHTTrWRd, docs/internals.md).
	if len(phys) > 0 {
		var rows []map[string]any
		for i, p := range phys {
			name := "Main"
			if i == 1 {
				name = "Secondary"
			} else if i > 1 {
				name = fmt.Sprintf("Port %d", i+1)
			}
			speed := p.Speed
			if !p.Up || speed < 0 {
				speed = 0
			}
			rows = append(rows, map[string]any{
				"port_idx": i + 1, "name": name, "media": "GE", "ifname": p.Name,
				"poe_caps": 0, "port_poe": false, "speed_caps": 1048623, // 10/100/1000, autoneg
				"enable": true, "autoneg": true, "op_mode": "switch",
				"up": p.Up, "speed": speed, "full_duplex": p.Up && p.FullDuplex,
				"is_uplink": i == 0 && p.Up,
				"rx_bytes":  p.RxBytes, "tx_bytes": p.TxBytes, "rx_packets": p.RxPkts, "tx_packets": p.TxPkts,
			})
		}
		m["port_table"] = rows
		m["has_eth1"] = len(phys) > 1
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
		if c, ok := caps[r.Band]; ok {
			rr := radios[len(radios)-1]
			rr["ieee_modes"], rr["radio_caps"], rr["radio_caps2"] = c.IEEEModes, c.RadioCaps, c.RadioCaps2
			if r.Band == "na" {
				rr["has_dfs"], rr["has_fccdfs"] = c.HasDFS, c.HasFCCDFS
			}
		}
		cuTotal, cuRx, cuTx := 0, 0, 0
		if prev != nil {
			if pr := prev.radio(r.Phy); pr != nil {
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
				vaps = append(vaps, uplinkVAP(vname, name, r, v, s.ARP))
				continue
			}
			guest := info.GuestVAP[vname]
			stas := []map[string]any{}
			satReal, satAnom := 0, 0 // sum of the clients' satisfaction_real, their anomaly bits
			for _, st := range v.Stations {
				row := map[string]any{
					"mac": st.MAC, "authorized": st.Authorized,
					"signal": st.Signal, "noise": st.Noise, "rssi": st.Signal - st.Noise,
					"tx_rate": st.TxRate, "rx_rate": st.RxRate,
					// AP's point of view: tx = to the client, rx = from it.
					"tx_bytes": st.TxBytes, "rx_bytes": st.RxBytes,
					"tx_packets": st.TxPackets, "rx_packets": st.RxPackets,
					"tx_retries": st.TxRetries,
					"uptime":     st.Connected, "idletime": st.Inactive / 1000,
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
	m["radio_table"] = radios
	m["radio_table_stats"] = rstats
	m["vap_table"] = vaps
	m["num_sta"] = nsta
	m["user-num_sta"] = nsta
	m["stat"] = map[string]any{"user-num_sta": nsta}
	return m
}

func (s *Snapshot) radio(phy string) *Radio {
	for i := range s.Radios {
		if s.Radios[i].Phy == phy {
			return &s.Radios[i]
		}
	}
	return nil
}

// uplinkPort: the wired port that is up (prefer a "lan" port, as on the
// OnHub where the cable is in its LAN port).
func (s *Snapshot) uplinkPort() *Port {
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
	for _, d := range translate.GuestPolicy(translate.ParseCfg(systemCfg)).Devnames {
		info.GuestVAP[d] = true
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

// parentDevice finds the parent AP's own MAC among the neighbours: UniFi VAP
// BSSIDs keep the device MAC's middle four bytes (5e:11:22:33:44:04 is a VAP of
// 40:11:22:33:44:01). Exactly one match, or nothing.
func parentDevice(bssid string, neigh map[string]string) string {
	b := strings.ToLower(bssid)
	if len(b) != 17 {
		return ""
	}
	found := ""
	for mac := range neigh {
		m := strings.ToLower(mac)
		if len(m) == 17 && m != b && m[3:14] == b[3:14] {
			if found != "" && found != m {
				return ""
			}
			found = m
		}
	}
	return found
}
