// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "fmt"

// planSyslog: the controller's remote syslog -> logd's log_ip/log_port/
// log_proto. UniFi-encrypted syslog (syslog.remote.encrypt, the console's own
// device-log intake on :5514) is sent by the daemon itself (syslogForwarder,
// device.SealSyslog), not by logd; any logd target set before is removed.
func planSyslog(cfg Cfg, cur UCI, r *Result) []Op {
	k := "system.@system[0]."
	want := map[string]string{}
	switch {
	case cfg["syslog.status"] != "enabled" || cfg["syslog.remote.status"] != "enabled" || cfg["syslog.remote.ip"] == "":
	case cfg["syslog.remote.encrypt"] == "enabled":
		r.Notes = append(r.Notes, fmt.Sprintf("syslog: remote %s:%s is UniFi-encrypted: sent by the daemon",
			cfg["syslog.remote.ip"], cfg["syslog.remote.port"]))
	default:
		port := cfg["syslog.remote.port"]
		if port == "" {
			port = "514"
		}
		want = map[string]string{"log_ip": cfg["syslog.remote.ip"], "log_port": port, "log_proto": "udp"}
	}
	var ops []Op
	for _, o := range []string{"log_ip", "log_port", "log_proto"} {
		v, ok := want[o]
		old, has := cur[k+o]
		switch {
		case ok && old != v:
			ops = append(ops, Op{Key: k + o, Value: v, Old: old})
		case !ok && has && o != "log_proto":
			ops = append(ops, Op{Del: true, Key: k + o, Old: old})
		}
	}
	return ops
}
