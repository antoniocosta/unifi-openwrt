// SPDX-License-Identifier: GPL-2.0-or-later

package inform

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// U7PG2 identity: UAP-AC-Pro (Gen2). Firmware string as reported by the real
// device open-unifi tested (fw 6.8.2.15592, build BZ.qca956x_6.8.2+15592.260126.1358).
const (
	U7PG2Model        = "U7PG2"
	U7PG2ModelDisplay = "UniFi AP-AC-Pro"
	U7PG2Version      = "6.8.2.15592"
	// PlaceholderFWCaps claims no feature: bits 0 and 1 are not among the
	// 22 fw_caps bits the controller tests (unifi-emu docs/PROTOCOL.md).
	PlaceholderFWCaps = 3
)

// Radio is one radio_table row (static radio facts).
type Radio struct {
	Name        string `json:"name"`  // wifi0 / wifi1 (Qualcomm naming, as on U7PG2)
	Radio       string `json:"radio"` // "ng" (2.4 GHz) or "na" (5 GHz)
	Channel     any    `json:"channel"`
	HT          string `json:"ht"` // "20", "40", "80"
	MinTxpower  int    `json:"min_txpower"`
	MaxTxpower  int    `json:"max_txpower"`
	NSS         int    `json:"nss"`
	TxPower     any    `json:"tx_power,omitempty"`
	TxPowerMode string `json:"tx_power_mode,omitempty"`
	RadioCaps   int    `json:"radio_caps"`
	AntennaGain int    `json:"antenna_gain"`
	BuiltinAnt  bool   `json:"builtin_antenna"`
	BuiltinGain int    `json:"builtin_ant_gain"`
}

// Device describes what we claim to be. Everything here comes from the
// OpenWrt AP or is made up (test devices).
type Device struct {
	MAC       net.HardwareAddr
	IP        string // dotted IPv4
	Netmask   string
	Gateway   string
	Hostname  string
	Serial    string // 12 upper-hex; a real U7PG2 uses its MAC
	InformURL string // must have an IP-literal host
	Uptime    int64
	Time      int64 // unix seconds
	Radios    []Radio
	// Model, ModelDisplay: the UniFi model reported (default U7PG2).
	Model, ModelDisplay string
}

// PendingInform builds the payload of an inform from an unadopted U7PG2
// (default key, state 1). Field list: docs/internals.md.
func PendingInform(d Device) ([]byte, error) {
	m, err := U7PG2Fields(d)
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// U7PG2Fields returns the pending-shape fields as a map, for callers that
// add the adopted-shape fields on top.
func U7PG2Fields(d Device) (map[string]any, error) {
	if len(d.MAC) != 6 {
		return nil, fmt.Errorf("u7pg2: MAC must be 6 bytes")
	}
	if ip := net.ParseIP(hostOf(d.InformURL)); ip == nil || ip.To4() == nil {
		return nil, fmt.Errorf("u7pg2: inform_url host must be an IPv4 literal: %q", d.InformURL)
	}
	mac := strings.ToLower(d.MAC.String())
	serial := d.Serial
	if serial == "" {
		serial = strings.ToUpper(strings.ReplaceAll(mac, ":", ""))
	}
	radios := d.Radios
	if radios == nil {
		radios = DefaultU7PG2Radios()
	}
	m := map[string]any{
		// No "_type": unifi-emu (proven on controller 10.4.57) sends none,
		// and open-unifi treats empty as a main inform.
		"mac":            mac,
		"serial":         serial,
		"model":          orDefault(d.Model, U7PG2Model),
		"model_display":  orDefault(d.ModelDisplay, U7PG2ModelDisplay),
		"display_name":   orDefault(d.ModelDisplay, U7PG2ModelDisplay),
		"type":           "uap",
		"version":        U7PG2Version,
		"board_rev":      0, // TODO: real U7PG2 value unknown
		"ip":             d.IP,
		"netmask":        d.Netmask,
		"gateway_ip":     d.Gateway,
		"hostname":       d.Hostname,
		"inform_url":     d.InformURL,
		"inform_ip":      hostOf(d.InformURL),
		"uptime":         d.Uptime,
		"time":           d.Time,
		"cfgversion":     "0",
		"state":          1,
		"default":        true,
		"_default_key":   true,
		"x_authkey":      DefaultKeyHex,
		"isolated":       false,
		"locating":       false,
		"selfrun_beacon": true,
		// Real U7PG2 bench value is 0xE7FD3F3F; the placeholder claims
		// nothing, which is the safe choice while pending (docs/internals.md).
		"fw_caps":     PlaceholderFWCaps,
		"uplink":      "eth0",
		"if_table":    []map[string]any{{"name": "eth0", "mac": mac, "ip": d.IP, "netmask": d.Netmask, "up": true}},
		"has_eth1":    false,
		"radio_table": radios,
		"vap_table":   []any{},
	}
	return m, nil
}

// DefaultU7PG2Radios: a UAP-AC-Pro is 3x3 on both bands (450 + 1300 Mbit/s);
// tx power ranges from unifi-emu's profile (controller hardware DB).
func DefaultU7PG2Radios() []Radio {
	return []Radio{
		{Name: "wifi0", Radio: "ng", Channel: "auto", HT: "20", MinTxpower: 5, MaxTxpower: 22, NSS: 3, TxPower: "auto", TxPowerMode: "auto", AntennaGain: 3, BuiltinAnt: true, BuiltinGain: 3},
		{Name: "wifi1", Radio: "na", Channel: "auto", HT: "80", MinTxpower: 5, MaxTxpower: 23, NSS: 3, TxPower: "auto", TxPowerMode: "auto", AntennaGain: 3, BuiltinAnt: true, BuiltinGain: 3},
	}
}

func hostOf(u string) string {
	s := u
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/"); i >= 0 {
		s = s[:i]
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
