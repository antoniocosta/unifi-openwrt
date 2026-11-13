// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "testing"

func TestSyslog(t *testing.T) {
	cur := ParseUCI("system.@system[0]=system\n")
	var r Result
	enc := ParseCfg("syslog.status=enabled\nsyslog.remote.status=enabled\nsyslog.remote.ip=192.168.1.1\nsyslog.remote.port=5514\nsyslog.remote.encrypt=enabled\n")
	if ops := planSyslog(enc, cur, &r); len(ops) != 0 || len(r.Notes) != 1 {
		t.Fatalf("encrypted: %v %v", ops, r.Notes)
	}
	plain := ParseCfg("syslog.status=enabled\nsyslog.remote.status=enabled\nsyslog.remote.ip=192.168.1.20\nsyslog.remote.port=514\n")
	if ops := planSyslog(plain, cur, &r); len(ops) != 3 {
		t.Fatalf("plain: %v", ops)
	}
	set := ParseUCI("system.@system[0]=system\nsystem.@system[0].log_ip='192.168.1.20'\nsystem.@system[0].log_port='514'\nsystem.@system[0].log_proto='udp'\n")
	if ops := planSyslog(plain, set, &r); len(ops) != 0 {
		t.Fatalf("applied: %v", ops)
	}
	if ops := planSyslog(enc, set, &r); len(ops) != 2 {
		t.Fatalf("back to encrypted removes target: %v", ops)
	}
}
