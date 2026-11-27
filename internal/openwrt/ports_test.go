// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"reflect"
	"testing"
	"time"
)

func TestParseFDB(t *testing.T) {
	got := parseFDB([]byte("aabbccddeeff 2 0 1234 0\n085566770001 1 1 0 0\n112233445566 3 0 5 1\nbad line\n"))
	want := []FDBEntry{
		{MAC: "aa:bb:cc:dd:ee:ff", Port: 2, Age: 12},
		{MAC: "08:55:66:77:00:01", Port: 1, Local: true},
		{MAC: "11:22:33:44:55:66", Port: 259},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseFDB = %+v, want %+v", got, want)
	}
}

func TestParseBridgePort(t *testing.T) {
	out := `=== board
{"hostname":"ap","model":"m","release":{"description":"OpenWrt"}}
=== port lan1
mac=08:55:66:77:00:01
speed=1000
duplex=full
oper=up
brport=0x3
=== fdb
aabbccddeeff 3 0 200 0
`
	s, err := Parse([]byte(out), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Ports) != 1 || s.Ports[0].BridgePort != 3 {
		t.Fatalf("ports = %+v", s.Ports)
	}
	if len(s.FDB) != 1 || s.FDB[0].Port != 3 || s.FDB[0].Age != 2 {
		t.Errorf("fdb = %+v", s.FDB)
	}
}

// A cable from the switch in the WAN port and a wired client in lan1: the
// gateway's MAC picks wan as the uplink, and lan1 carries the client.
func TestPortTableWiredClients(t *testing.T) {
	s := &Snapshot{
		MAC: "08:55:66:77:00:01", Gateway: "192.168.1.1",
		ARP: map[string]string{"40:11:22:33:44:01": "192.168.1.1"},
		Ports: []Port{
			{Name: "lan1", MAC: "08:55:66:77:00:01", Up: true, Speed: 1000, BridgePort: 1},
			{Name: "lan2", MAC: "08:55:66:77:00:01", BridgePort: 2},
			{Name: "wan", MAC: "08:55:66:77:00:04", Up: true, Speed: 1000, BridgePort: 4},
		},
		FDB: []FDBEntry{
			{MAC: "40:11:22:33:44:01", Port: 4, Age: 1},
			{MAC: "08:55:66:77:00:01", Port: 1, Local: true},
			{MAC: "aa:bb:cc:dd:ee:01", Port: 1, Age: 30},
			{MAC: "aa:bb:cc:dd:ee:01", Port: 1, Age: 3},
			{MAC: "aa:bb:cc:dd:ee:02", Port: 4, Age: 5},
		},
	}
	rows, _ := s.Fields(nil, CfgInfo{}, nil)["port_table"].([]map[string]any)
	if len(rows) != 3 || rows[0]["ifname"] != "wan" || rows[0]["is_uplink"] != true {
		t.Fatalf("port_table = %v", rows)
	}
	if _, ok := rows[0]["mac_table"]; ok {
		t.Errorf("the uplink port must not carry a mac_table: %v", rows[0])
	}
	want := []map[string]any{{"mac": "aa:bb:cc:dd:ee:01", "age": int64(3), "vlan": 1, "static": false}}
	if rows[1]["ifname"] != "lan1" || !reflect.DeepEqual(rows[1]["mac_table"], want) {
		t.Errorf("lan1 = %v, want mac_table %v", rows[1], want)
	}
	if mt, ok := rows[2]["mac_table"].([]map[string]any); rows[2]["ifname"] != "lan2" || !ok || len(mt) != 0 {
		t.Errorf("lan2 = %v, want an empty mac_table", rows[2])
	}
}

// An in-wall model: front ports from 1, the uplink jack as port 5.
func TestPortTableRearUplink(t *testing.T) {
	s := &Snapshot{RearUplink: 5, UplinkName: "wan", Ports: []Port{
		{Name: "lan1", MAC: "08:55:66:77:00:01", Up: true, BridgePort: 1},
		{Name: "lan2", MAC: "08:55:66:77:00:01", BridgePort: 2},
		{Name: "wan", MAC: "08:55:66:77:00:04", Up: true, BridgePort: 4},
	}}
	rows, _ := s.Fields(nil, CfgInfo{}, nil)["port_table"].([]map[string]any)
	var got []any
	for _, r := range rows {
		got = append(got, r["port_idx"], r["name"], r["ifname"], r["is_uplink"])
	}
	want := []any{1, "Port 1", "lan1", false, 2, "Port 2", "lan2", false, 5, "PoE In + Data", "wan", true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("port_table = %v, want %v", got, want)
	}
	// AC-IW (uplink port 3) on an AP with three LAN ports: lan3 goes after it.
	s.RearUplink = 3
	s.Ports = append(s.Ports, Port{Name: "lan3", MAC: "08:55:66:77:00:01", BridgePort: 3})
	rows, _ = s.Fields(nil, CfgInfo{}, nil)["port_table"].([]map[string]any)
	got = nil
	for _, r := range rows {
		got = append(got, r["port_idx"], r["ifname"])
	}
	if want := []any{1, "lan1", 2, "lan2", 3, "wan", 4, "lan3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("AC-IW port_table = %v, want %v", got, want)
	}
	s.RearUplink = 0
	rows, _ = s.Fields(nil, CfgInfo{}, nil)["port_table"].([]map[string]any)
	if rows[0]["port_idx"] != 1 || rows[0]["name"] != "Main" || rows[0]["ifname"] != "wan" {
		t.Errorf("without RearUplink the uplink stays port 1 Main: %v", rows[0])
	}
}

// -uplink-port wins over the gateway's FDB entry.
func TestUplinkPortForced(t *testing.T) {
	s := &Snapshot{
		Gateway: "192.168.1.1", ARP: map[string]string{"40:11:22:33:44:01": "192.168.1.1"},
		Ports: []Port{{Name: "lan1", Up: true, BridgePort: 1}, {Name: "wan", Up: true, BridgePort: 4}},
		FDB:   []FDBEntry{{MAC: "40:11:22:33:44:01", Port: 1}},
	}
	if p := s.uplinkPort(); p == nil || p.Name != "lan1" {
		t.Fatalf("uplinkPort = %+v, want lan1 (gateway)", p)
	}
	s.UplinkName = "wan"
	if p := s.uplinkPort(); p == nil || p.Name != "wan" {
		t.Errorf("uplinkPort = %+v, want wan (forced)", p)
	}
}

// Without the gateway in the FDB the old rule stays: an up "lan" port first.
func TestUplinkPortFallback(t *testing.T) {
	s := &Snapshot{Gateway: "192.168.1.1", Ports: []Port{
		{Name: "wan", Up: true, BridgePort: 4},
		{Name: "lan1", Up: true, BridgePort: 1},
	}}
	if p := s.uplinkPort(); p == nil || p.Name != "lan1" {
		t.Errorf("uplinkPort = %+v, want lan1", p)
	}
}

func TestDownlinkPortsAndUplinkPortName(t *testing.T) {
	// Default uplink layout: uplink is port 1, downlinks start at 2.
	s := &Snapshot{
		MAC: "02:00:00:00:00:01", Gateway: "192.0.2.1",
		ARP: map[string]string{"02:00:00:00:00:fe": "192.0.2.1"},
		Ports: []Port{
			{Name: "lan1", MAC: "02:00:00:00:00:01", Up: true, BridgePort: 1},
			{Name: "lan2", MAC: "02:00:00:00:00:01", BridgePort: 2},
			{Name: "wan", MAC: "02:00:00:00:00:04", Up: true, BridgePort: 4},
		},
		FDB: []FDBEntry{{MAC: "02:00:00:00:00:fe", Port: 4, Age: 1}},
	}
	if got := s.UplinkPortName(); got != "wan" {
		t.Errorf("UplinkPortName = %q, want %q", got, "wan")
	}
	if got, want := s.DownlinkPorts(), map[int]string{2: "lan1", 3: "lan2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("default DownlinkPorts = %v, want %v", got, want)
	}

	// In-wall rear uplink at port 5: front ports start at 1.
	iw := &Snapshot{
		RearUplink: 5, UplinkName: "wan",
		Ports: []Port{
			{Name: "lan1", MAC: "02:00:00:00:00:01", Up: true, BridgePort: 1},
			{Name: "lan2", MAC: "02:00:00:00:00:01", BridgePort: 2},
			{Name: "wan", MAC: "02:00:00:00:00:04", Up: true, BridgePort: 4},
			{Name: "teql0"}, // virtual device (no MAC) is excluded
		},
	}
	if got := iw.UplinkPortName(); got != "wan" {
		t.Errorf("in-wall UplinkPortName = %q, want %q", got, "wan")
	}
	if got, want := iw.DownlinkPorts(), map[int]string{1: "lan1", 2: "lan2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RearUplink=5 DownlinkPorts = %v, want %v", got, want)
	}

	// In-wall rear uplink at port 3 with 3 LAN ports: lan3 skips port 3 to 4.
	iw.RearUplink = 3
	iw.Ports = append(iw.Ports, Port{Name: "lan3", MAC: "02:00:00:00:00:01", BridgePort: 3})
	if got, want := iw.DownlinkPorts(), map[int]string{1: "lan1", 2: "lan2", 4: "lan3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("RearUplink=3 DownlinkPorts = %v, want %v", got, want)
	}

	// Single-port AP has no downlink ports.
	single := &Snapshot{Ports: []Port{{Name: "eth0", MAC: "02:00:00:00:00:01", Up: true}}}
	if got := single.UplinkPortName(); got != "eth0" {
		t.Errorf("single-port UplinkPortName = %q, want %q", got, "eth0")
	}
	if got := single.DownlinkPorts(); got != nil {
		t.Errorf("single-port DownlinkPorts = %v, want nil", got)
	}

	// When no uplink can be resolved, port 0 must not be silently dropped.
	noUp := &Snapshot{
		RearUplink: 5,
		Ports: []Port{
			{Name: "lan1", MAC: "02:00:00:00:00:01"},
			{Name: "lan2", MAC: "02:00:00:00:00:02"},
		},
	}
	if got := noUp.UplinkPortName(); got != "" {
		t.Errorf("unresolved UplinkPortName = %q, want empty", got)
	}
	if got, want := noUp.DownlinkPorts(), map[int]string{1: "lan1", 2: "lan2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("unresolved DownlinkPorts = %v, want %v", got, want)
	}
}

func TestPortTableMACVLAN(t *testing.T) {
	cfg := "switch.status=enabled\nswitch.vlan.status=enabled\n" +
		"switch.port.1.pvid=40\nswitch.port.2.pvid=20\n" +
		"switch.port.3.pvid=0\nswitch.port.4.pvid=4095\nswitch.port.5.pvid=+40\nswitch.port.06.pvid=30\n"
	info := InfoFromCfg(cfg)
	if got, want := info.PortVLAN, map[int]int{1: 40, 2: 20}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PortVLAN = %v, want %v", got, want)
	}
	s := &Snapshot{
		RearUplink: 3, UplinkName: "wan",
		MAC: "02:00:00:00:00:01",
		Ports: []Port{
			{Name: "lan1", MAC: "02:00:00:00:00:01", Up: true, Speed: 1000, BridgePort: 1},
			{Name: "lan2", MAC: "02:00:00:00:00:02", Up: true, Speed: 1000, BridgePort: 2},
			{Name: "wan", MAC: "02:00:00:00:00:03", Up: true, Speed: 1000, BridgePort: 3},
		},
		FDB: []FDBEntry{
			{MAC: "02:00:00:00:00:11", Port: 1, Age: 5},
			{MAC: "02:00:00:00:00:22", Port: 2, Age: 7},
		},
	}
	rows, _ := s.Fields(nil, info, nil)["port_table"].([]map[string]any)
	want1 := []map[string]any{{"mac": "02:00:00:00:00:11", "age": int64(5), "vlan": 40, "static": false}}
	want2 := []map[string]any{{"mac": "02:00:00:00:00:22", "age": int64(7), "vlan": 20, "static": false}}
	if !reflect.DeepEqual(rows[0]["mac_table"], want1) {
		t.Errorf("port 1 mac_table = %v, want %v", rows[0]["mac_table"], want1)
	}
	if !reflect.DeepEqual(rows[1]["mac_table"], want2) {
		t.Errorf("port 2 mac_table = %v, want %v", rows[1]["mac_table"], want2)
	}
	// Disabled switch VLAN falls back to VLAN 1.
	infoOff := InfoFromCfg("switch.status=disabled\nswitch.vlan.status=disabled\nswitch.port.1.pvid=40\n")
	if len(infoOff.PortVLAN) != 0 {
		t.Errorf("disabled PortVLAN = %v, want empty", infoOff.PortVLAN)
	}
	rowsOff, _ := s.Fields(nil, infoOff, nil)["port_table"].([]map[string]any)
	wantOff := []map[string]any{{"mac": "02:00:00:00:00:11", "age": int64(5), "vlan": 1, "static": false}}
	if !reflect.DeepEqual(rowsOff[0]["mac_table"], wantOff) {
		t.Errorf("disabled port 1 mac_table = %v, want %v", rowsOff[0]["mac_table"], wantOff)
	}
}
