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
