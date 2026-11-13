// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "testing"

func TestNetworkStaticAndBack(t *testing.T) {
	base := "netconf.status=enabled\nnetconf.1.status=enabled\nnetconf.1.devname=br0\nconnectivity.uplink_bridge=br0\ndhcpc.status=enabled\n"
	static := base + "netconf.1.ip=192.168.1.2\nnetconf.1.netmask=255.255.255.0\nroute.status=enabled\nroute.1.status=enabled\nroute.1.devname=br0\nroute.1.ip=0.0.0.0\nroute.1.gateway=192.168.1.1\nresolv.status=enabled\nresolv.nameserver.1.ip=192.168.1.53\n"
	dhcp := base + "netconf.1.ip=0.0.0.0\ndhcpc.1.status=enabled\ndhcpc.1.devname=br0\nresolv.nameserver.1.status=disabled\n"
	curDHCP := "network.lan=interface\nnetwork.lan.device='br-lan'\nnetwork.lan.proto='dhcp'\n"
	var r Result
	ops := planNetwork(ParseCfg(static), ParseUCI(curDHCP), "192.168.1.2", &r)
	got := map[string]string{}
	for _, o := range ops {
		got[o.Key] = o.Value
		if o.List != nil {
			got[o.Key] = o.List[0]
		}
	}
	want := map[string]string{"network.lan.proto": "static", "network.lan.ipaddr": "192.168.1.2", "network.lan.netmask": "255.255.255.0", "network.lan.gateway": "192.168.1.1", "network.lan.dns": "192.168.1.53"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (ops %v)", k, got[k], v, ops)
		}
	}
	// a different address is refused
	r = Result{}
	if ops := planNetwork(ParseCfg(static), ParseUCI(curDHCP), "192.168.1.99", &r); len(ops) != 0 || len(r.Notes) != 1 {
		t.Fatalf("other address: ops %v notes %v", ops, r.Notes)
	}
	// back to DHCP removes the static options
	curStatic := curDHCP + "network.lan.proto='static'\nnetwork.lan.ipaddr='192.168.1.2'\nnetwork.lan.netmask='255.255.255.0'\nnetwork.lan.gateway='192.168.1.1'\nnetwork.lan.dns='192.168.1.53'\n"
	ops = planNetwork(ParseCfg(dhcp), ParseUCI(curStatic), "192.168.1.2", &r)
	if len(ops) != 5 {
		t.Fatalf("to dhcp: %v", ops)
	}
}
