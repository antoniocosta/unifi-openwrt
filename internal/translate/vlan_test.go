// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "testing"

func TestVLAN(t *testing.T) {
	cfg := Cfg{
		"wireless.1.devname": "wifi0ap0", "wireless.1.ssid": "home",
		"wireless.6.devname": "wifi0ap5", "wireless.6.ssid": "test",
		"bridge.1.devname": "br0", "bridge.1.port.1.devname": "eth0", "bridge.1.port.2.devname": "wifi0ap0",
		"bridge.2.devname": "br0.77", "bridge.2.port.1.devname": "wifi0ap5", "bridge.2.port.3.devname": "eth0.77",
	}
	netOf, ids := vlanNetworks(cfg)
	if netOf["wifi0ap5"] != "uow77" || netOf["wifi0ap0"] != "" || len(ids) != 1 || ids[0] != "77" {
		t.Fatalf("vlanNetworks: %v %v", netOf, ids)
	}
	cur := UCI{"network.cfg030f15": "device", "network.cfg030f15.name": "br-lan", "network.cfg030f15.ports": "lan1",
		"network.lan": "interface", "network.uow12": "interface", "network.uowvlan12": "device", "network.uowvlan12.name": "br-uow12"}
	var r Result
	got := map[string]string{}
	for _, o := range planVLAN(cfg, cur, &r) {
		switch {
		case o.Del:
			got[o.Key] = "<del>"
		case o.List != nil:
			got[o.Key] = o.List[0]
		default:
			got[o.Key] = o.Value
		}
	}
	want := map[string]string{
		"network.uowvlan77": "device", "network.uowvlan77.name": "br-uow77", "network.uowvlan77.type": "bridge",
		"network.uowvlan77.ports": "lan1.77", "network.uow77": "interface", "network.uow77.device": "br-uow77",
		"network.uow77.proto": "none", "network.uow12": "<del>", "network.uowvlan12": "<del>",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("ops: %v", got)
	}
	if _, ok := got["network.lan"]; ok {
		t.Error("network.lan must not be touched")
	}
	// nothing to do once it is in place
	cur2 := UCI{"network.d": "device", "network.d.name": "br-lan", "network.d.ports": "lan1",
		"network.uowvlan77": "device", "network.uowvlan77.name": "br-uow77", "network.uowvlan77.type": "bridge",
		"network.uowvlan77.ports": "lan1.77", "network.uow77": "interface", "network.uow77.device": "br-uow77", "network.uow77.proto": "none"}
	if ops := planVLAN(cfg, cur2, &r); len(ops) != 0 {
		t.Errorf("idempotent: %v", ops)
	}
}
