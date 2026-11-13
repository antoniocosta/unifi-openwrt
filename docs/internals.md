# Internals

How unifi-openwrt works, for contributors. Everything here was worked out against
UniFi Network 10.6 and UAP firmware 6.8.2; other versions may differ.

- [Protocol: inform, adoption, keys](#protocol-inform-adoption-keys)
- [Config translation: system_cfg to UCI](#config-translation-systemcfg-to-uci)
- [Stats in the inform](#stats-in-the-inform)
- [UniFi AP "satisfaction" / WiFi Experience score: how it is computed](#unifi-ap-satisfaction--wifi-experience-score-how-it-is-computed)
- [Models and capability profiles](#models-and-capability-profiles)
- [Status LED](#status-led)
- [Mesh (wireless uplink)](#mesh-wireless-uplink)
- [VLANs](#vlans)
- [Guest WLANs and the guest portal](#guest-wlans-and-the-guest-portal)
- [RF scan and 802.11k neighbour list](#rf-scan-and-80211k-neighbour-list)

## Protocol: inform, adoption, keys

How unifi-openwrt talks to a UniFi controller as a UniFi AP. Code:
`internal/inform` (packets, payload), `internal/device` (session, replies,
SSH adoption, syslog). Sources: open-unifi `docs/PROTOCOL*.md`, unifi-emu
`docs/PROTOCOL.md`, and what UniFi Network 10.6 did on the wire. Class names
like `gliBtHTTrWRd` are obfuscated names in the controller's
`lib/internal/internal-dependencies.jar` (Network 10.6.109); they change
between releases.

### Inform packet

The device POSTs a binary frame to `http://<controller>:8080/inform` every
few seconds; the controller answers with a frame of the same format.

```
off size field
0   4    magic "TNBU"
4   4    packet version (big endian); devices send 0, nobody checks it
8   6    device MAC (must equal the payload's "mac", or HTTP 400)
14  2    flags: 0x01 encrypted, 0x02 zlib, 0x04 snappy, 0x08 AES-GCM
16  16   IV (also the GCM nonce: 16 bytes)
32  4    data version, must be 1
36  4    payload length (for GCM including the 16-byte tag)
40  n    payload
```

- CBC: AES-128-CBC with PKCS#7 padding. GCM: AES-128-GCM with a 16-byte
  nonce, the whole 40-byte header (length filled in) as additional data, tag
  appended.
- The key is the hex-decoded 32-hex-digit `authkey` (no key derivation).
  Before adoption every device uses the public factory key, MD5 of `ubnt`.
- A real UAP-AC-Pro sends flags `0x0003` (CBC + zlib) while pending and
  `0x000b` (GCM + zlib) once adopted. Replies come back uncompressed
  (`0x0001` or `0x0009`) with a fresh IV and the same key.
- Plain-text informs are rejected; bodies over 10 MB too.
- HTTP 404 to a pending device is normal: keep informing.
- `inform_url` must have an IP literal as host; the controller answers
  `invalid inform_ip` to a host name.

`internal/inform/interop_test.go` checks the codec against open-unifi's
controller-side decoder and unifi-emu's device-side decoder (set
`INFORM_INTEROP_OUT` / `INFORM_INTEROP_IN`).

### Payload fields

JSON, one object. Sent on every inform, pending or adopted:

| field | value |
|---|---|
| `mac` | the AP's MAC, lowercase, colon hex |
| `serial` | MAC in upper hex without colons |
| `model`, `model_display`, `version` | from the chosen model (default `U7PG2`, `UniFi AP-AC-Pro`, `6.8.2.15592`) |
| `ip`, `hostname`, `netmask`, `gateway` | the AP's |
| `inform_url` | the controller URL (IP literal) |
| `uptime`, `time` | seconds since boot, Unix seconds |
| `cfgversion` | `"0"` while pending, then the controller's; a mismatch makes the controller re-provision |
| `default`, `_default_key` | `true` until adopted |
| `state` | 1 pending, 4 adopted (not read by the controller) |
| `fw_caps` | `3` (nothing) while pending; the model profile's value once adopted. Send it as an unsigned 32-bit number |
| `sshd_port` | where the controller SSHes to adopt (see below) |
| `inform_min_interval` | `1`: the device supports live updates (see `docs/stats.md`) |

Do not send `udapi_caps` without `udapi_version` (the controller then drops all
capabilities); a U7PG2 sends neither. Send no `_type` for a normal inform.

Once adopted the inform also carries `radio_table`, `vap_table` (with
`sta_table` per VAP), `if_table`, `port_table`, `sys_stats`, `system-stats`,
`stat`, `satisfaction`, `hash_id`, `guest_token` and the capability words.
See `docs/stats.md`.

### Adoption

Network 10.6 uses two paths; handle both.

1. **SSH.** After the user clicks Adopt, the controller SSHes to the
   device's `ip` at `sshd_port` (read from the inform), logs in as `ubnt`
   and runs `syswrapper.sh set-adopt http://<controller>:8080/inform <authkey>`.
   Its SSH client offers only RSA host-key algorithms (`rsa-sha2-512`,
   `rsa-sha2-256`, `ssh-rsa`). unifi-openwrt runs a minimal SSH responder
   (`internal/device/sshadopt.go`, default port 2222) that accepts this one
   command and nothing else. If the login fails the device lands in
   "adoption failed" and only Remove helps.
2. **Inform reply.** A `setparam` whose `mgmt_cfg` holds `authkey=` and
   `use_aes_gcm=true`. Accept a new key from `mgmt_cfg` only while still on
   the factory key (otherwise a device can get stuck re-adopting).

After the key: a `setparam` with `mgmt_cfg` (cfgversion, LED, STUN and
management URLs) and then the full `system_cfg` (several KB of
`section.N.key=value` lines; it contains WLAN passphrases, so it is stored
mode 600 and never logged). The device echoes the new `cfgversion` and the
controller shows it as connected.

### Replies to handle

- `noop` with `interval` (seconds until the next inform).
- `setparam`: `mgmt_cfg`, `system_cfg`, `blocked_sta` (newline-separated MACs).
- `cmd`: `set-adopt`, `set-locate` / `unset-locate`, `kick-sta`,
  `block-sta` / `unblock-sta` (with `mac`), `authorize-guest` /
  `unauthorize-guest`, `scan`, `spectrum-scan`, `reboot`, `setdefault`.
- Top-level `_type` `reboot`, `setdefault`, `upgrade`.

**`upgrade` is never applied.** Ubiquiti firmware would brick an OpenWrt
device. The daemon downloads nothing; it reports the requested version and
resets its uptime (a faked reboot, as unifi-emu does), so the controller
does not loop on the upgrade.

**`setdefault`** (the user clicked Remove) means "forget the controller":
the daemon goes back to the factory key, `cfgversion` "0" and no
`mgmt_cfg`, i.e. pending. It never factory-resets OpenWrt and leaves the
AP's Wi-Fi as it is.

### Encrypted remote syslog

With the site's syslog set to "this controller, encrypted only", the
`# syslog` section of system_cfg is only sent to devices whose `fw_caps` has
bit `0x1000000`. The controller's listener (UDP 5514) takes one datagram per
message (at most 4096 bytes):

```
AB CD DC BA | 01 | 00 00 00 01 | hash_id (8 bytes) | nonce (12 bytes) | AES-256-GCM(ciphertext || tag)
```

The additional data is bytes 0-28. The key is the device's `syslog_key`
(`syslog.remote.key`, 64 hex digits). The controller finds the device by
`hash_id` (16 hex digits, not all zero) that the device reports in its
informs. Lines are tagged `MODEL,mac12,version:` like a UAP's. Code:
`internal/device/syslog.go` (`SealSyslog`).

Unencrypted remote syslog (a syslog server set in the UniFi) is translated to
logd's `log_ip` / `log_port` instead.

## Config translation: system_cfg to UCI

The controller provisions a UniFi AP with `system_cfg`: flat
`section.N.key=value` lines (several KB). `internal/translate` turns it into
OpenWrt UCI changes; `internal/openwrt` applies them and runs the parts that
are not UCI (nftables, tc, usteer, snmpd, hostapd runtime lists).

### How a change is applied

1. **Plan.** `translate.Plan(system_cfg, current UCI, features)` returns a
   list of UCI operations plus notes (clamps, skipped sections). Secrets
   (passphrases, password hashes) are compared by hash and never printed.
2. **Log-only by default.** Without `-apply` (UCI `option apply '0'`) the
   daemon only logs the plan.
3. **Apply with rollback.** Operations go to `uci batch` on stdin (never in
   a command line), then `uci commit` and a reload of the touched service
   (`wifi reload`, network reload detached, system reload). Before that the
   daemon backs up the touched packages and starts an on-AP timer that
   restores them after 5 minutes unless confirmed.
4. **Health check.** 20 s later: every enabled radio that has interfaces must
   be up with its interfaces (`ubus call network.wireless status`). OK ->
   confirm (the timer exits, backups are removed); not OK -> roll back at
   once. A radio left with no interfaces (Disable device) is fine.

The translator only disables AP interfaces the controller no longer lists;
it never deletes them.

### Feature probe

`openwrt.Client.Features` checks the AP's hostapd for what it can do:
802.11r (`hostapd -v11r`), and 802.11k/v from the binary's config keys
(`rrm_neighbor_report`, `bss_transition`; `-v11k` / `-v11v` are not real
probes). OpenWrt's `wpad-basic-*` lacks 802.11k/v; install the full `wpad-*`.
A requested feature the AP lacks is left off and noted ("clamped").

### Mapping

| system_cfg | OpenWrt |
|---|---|
| `radio.N.channel`, `ieee_mode` (`11nght20`, `11naht40`, `11acvht80`, ...), `txpower` (`auto` or dBm), `countrycode` | `wifi-device` `channel`, `htmode`, `txpower` (deleted for auto), `country`. Radios are matched by band, not by phy number (phys can renumber after a reboot) |
| `radio.outdoor=enabled` | `country3 0x4f` + `ieee80211d 1` (the Country IE environment byte; channel rules stay the country's) |
| `wireless.N` + `aaa.N` (one per radio x WLAN) | a `wifi-iface` per radio: `ssid`, `hidden`, `isolate` (`l2_isolation`), `uapsd`, `dtim_period`, `disabled`, `network`. Matched to an existing interface by SSID and radio, else created as `unifi_<radio>_<N>` |
| `aaa.N.wpa`, `wpa.key.N.mgmt`, `wpa3.support`, `wpa3.transition`, `pmf.*` | `encryption` (`psk2+ccmp`, `sae`, `sae-mixed`, ...), `ieee80211w`, `key`. A WPA2/WPA3 WLAN arrives as `mgmt=SAE` plus `wpa3.transition=enabled` |
| key management | `hostapd_bss_options wpa_key_mgmt=SAE WPA-PSK` (+ `FT-SAE FT-PSK` with 802.11r). OpenWrt adds `WPA-PSK-SHA256` whenever PMF is optional; a UAP does not, and some older Apple clients skip a BSS that offers it |
| `aaa.N.ft.status`, `11k.status`, `bss_transition` | `ieee80211r`, `ieee80211k`, `bss_transition` (if the probe allows) |
| `macacl.N.*` (keyed by VAP devname) | `macfilter allow/deny` + `maclist`; no filter = options deleted (OpenWrt rejects `macfilter disable`) |
| `wireless.N.minrate_data`, `beacon_rate`, `mgmt_rate`, `minrate_cck_rates` | radio `supported_rates` / `basic_rate`, only when every WLAN on the radio raised its minimum (OpenWrt applies rates per radio; per-BSS beacon rates need driver support) |
| `wireless.N.schedule_*` | AP-side cron script `/etc/unifi-openwrt/schedule.sh` that flips the interface; the controller does not toggle scheduled WLANs itself |
| `stamgr.N.minrssi.*` | radio `rssi_reject_assoc_rssi`; the daemon also kicks clients below the minimum for 30 s. `minrssi.rssi` may arrive as RSSI above noise (value - 95 = dBm) |
| `stamgr.N.loadbalance.*` | `maxassoc` on the radio's UniFi interfaces |
| `bandsteering.*` | usteer in local mode (`prefer_5g`, `equal`), stopped when off |
| `qos.vap.N.*` (user-group rate limits, kbps) | per-client tc HTB shaping on the VAP (download) and an IFB (upload); nftables policing if tc is missing |
| `aaa.N.is_guest`, `ebtables`, `iptables`, `ipset`, `redirector` | nftables bridge table for guest isolation, guest DNS forwarder, portal redirect. See `docs/guest.md` |
| `vlan.N`, `bridge.N` | per-VLAN bridge on the cable port. See `docs/vlan.md` |
| `mesh.*`, the `vwire` VAP | mesh-uplink watcher. See `docs/mesh.md` |
| `system.timezone`, `ntpclient.N.server` | `system.@system[0].timezone`, `system.ntp.server` |
| `netconf`, `dhcpc`, `route`, `resolv` (management IP) | `network.lan` `proto dhcp` or `static` (`ipaddr`, `netmask`, `gateway`, `dns`). A static address other than the one the daemon reaches the AP on is refused |
| `users.N` (site Device SSH Authentication, sha512-crypt) | extra uid-0 logins in `/etc/passwd` / `/etc/shadow`; only accounts listed in `/etc/unifi-openwrt/users` are touched, never root or system accounts |
| `syslog.remote.*` | plain: logd `log_ip` / `log_port` / `log_proto`; encrypted: the daemon forwards itself (`docs/protocol.md`) |
| `snmp.*` (`version`, `community`, `user.N`) | `/etc/unifi-openwrt/snmpd.conf` (mode 600) and an snmpd procd instance while it exists. OpenWrt's `snmpd` package is `snmpd-nossl`, so SNMPv3 auth/priv is not available |
| `ledbar.*` | LED colour and brightness (`docs/led.md`) |

Radios the controller remembers but the AP does not report are ignored (the
controller never drops a radio row once reported, so get the radio list
right before the first adoption).

### Client actions

- **Block / Unblock** (`cmd block-sta` / `unblock-sta`, and the `blocked_sta`
  list): `hostapd_cli deny_acl ADD_MAC` / `DEL_MAC` on every BSS, re-asserted
  every 30 s and after each Wi-Fi reload (needs `hostapd-utils`). Without
  hostapd_cli: a 90 s `del_client` ban, refreshed.
- **Reconnect** (`kick-sta`): `del_client` with ban time 0.
- **Disable device**: the controller sends a config without WLANs and LED
  off; the translator disables the AP interfaces.

## Stats in the inform

What the daemon reads on the AP and where the controller expects it. Code:
`internal/openwrt/collect.go` (reading), `internal/openwrt/unifi.go` (inform
fields), `internal/openwrt/satisfaction.go`.

### Reading the AP

Every inform: `ubus call system board` / `info`, `ubus call iwinfo devices`
then `info`, `assoclist`, `txpowerlist` per interface, `iw survey dump`,
antenna masks, `/sys/class/net/*/statistics`, `/proc/net/arp`, `/proc/stat`.
One reading takes about 0.2 s on a dual-core ARM AP. Interfaces are
discovered each time (phy numbers can change after a reboot); the daemon's
own scan interface `uowscan*` is skipped.

### Radios: `radio_table`

One row per phy, named `wifi0`, `wifi1`, ... (`ng` = 2.4 GHz, `na` = 5 GHz,
`6e` = 6 GHz). Fields: `name`, `radio`, `channel`, `ht`, `tx_power`,
`min_txpower` / `max_txpower`, `nss`, `radio_caps`, `is_11ac`, `has_dfs`, ...
from the model profile and the hardware.

Channel use goes in **`radio_table[].athstats`** (`cu_total`, `cu_self_rx`,
`cu_self_tx`, `cu_interf`, percent), not `radio_table_stats`: that is what the
controller's UAP inform reader (`gliBtHTTrWRd`) and the history sampler read.
From the survey counters: total = busy / active, self_rx = receive / active,
self_tx = transmit / active, interf = total - self_rx - self_tx, as deltas
between informs, rounded to the nearest percent.

### VAPs and clients: `vap_table`

One row per AP interface, named after the controller's devname for that
WLAN and radio (`wireless.N.devname`, e.g. `wifi0ap0`), else `ath0`, `ath1`,
... across radios. Fields: `state: "RUN"` (anything else counts as not
running), `essid`, `bssid`, `radio`, `radio_name`, `channel`, `up`, `usage`
(`user`; `uplink` for the mesh uplink), `is_guest`, `id` (the WLAN id from
system_cfg), byte and packet counters, `tx_retries`, `wifi_tx_attempts`,
`wifi_tx_dropped`, `avg_client_signal`, `satisfaction`.

Clients are only in `vap_table[].sta_table[]` (never top level): `mac`,
`signal`, `rssi` (signal + 95), `noise`, `tx_rate`, `rx_rate`, `uptime`,
`tx_bytes`, `rx_bytes`, `tx_retries`, `ip` (from the ARP table),
**`chwidth`** (an index: 0 = 20, 1 = 40, 2 = 80, 3 = 160 MHz; the larger of the rx and tx widths),
`satisfaction`, `anomalies`, `authorized` (guests).

The controller hides its own element-adopt and mesh VAPs in its device
view but keeps the user ones.

### Ports: `if_table`, `port_table`

`if_table[0]` (`eth0`, with `num_port`) and `port_table` (port 1 = the
uplink port, then the others: `media`, `speed`, `full_duplex`, `up`,
`is_uplink`, counters). The controller builds `ethernet_table` from these and
keeps `port_stats`. It never drops a stored port row. A UAP-AC-Pro has no
port squares in the panel (that needs a `ports` entry in the panel's model
data), only the Port Manager button.

For a wired uplink the `eth0` row carries `signal 0`, `rssi 0`,
`is_mesh_v3 false`: the controller merges the row into the stored uplink,
and old mesh values would otherwise keep a mesh signal bar on the map.

### System

- `sys_stats` (`loadavg_*`, `mem_*`) and **`system-stats`** (`cpu`, `mem`,
  `uptime`, as strings; CPU % from `/proc/stat` deltas, memory % = (total -
  free - buffers - cache) / total). Without `system-stats` the CPU and memory
  graphs stay empty.
- `satisfaction` at the top level, per VAP and in `athstats.satisfaction`:
  see `docs/satisfaction-formula.md` for how UAP firmware computes it.
- `hash_id` (encrypted syslog), `guest_token` and `guest_kicks` (portal).

### Live updates

The client panel draws its live signal graph, and the device panel its live
bars, only for devices that report **`inform_min_interval: 1`** (stored as
`min_inform_interval_seconds`; `supportsLiveUpdates()` is "== 1"). The
controller then answers with `interval: 1` while someone watches the device,
and the daemon follows the reply's interval down to 1 s. `-inform-every`
(UCI `inform_every`) sets a minimum rate of its own.

## UniFi AP "satisfaction" / WiFi Experience score: how it is computed

Static analysis of public files only: nothing from them was executed.

### Summary

- The score is computed **on the AP**, in `libubnt.so` (`calc_sta_satisfaction` and friends), and serialized by `mcad`. The controller (Network 10.6.109) only reads, averages and thresholds it.
- Per client it is the **minimum of a set of 0-100 sub-scores**, not a weighted sum. Each sub-score is averaged over up to 5 samples, the minimum is taken, and the reported `satisfaction` is that value **clamped to 60..100** (or -1).
- **Signal strength is not an input** in firmware 6.8.2. The wireless inputs are effective TX PHY rate (5 GHz only), TX retry share, TX drop share and TCP latency; the rest are connectivity checks (DHCP/ARP/DNS/ICMP/disconnects).
- VAP = mean of its clients; radio and AP = client-count-weighted mean of VAPs.
- `cu_*` come straight from the Wi-Fi driver through a private ioctl; userland only range-checks them.

### Sources analysed

| What | Where |
|---|---|
| Controller 10.6.109 | `usr/lib/unifi/lib/internal/internal-dependencies.jar` from the public `unifi_sysvinit_all.deb`, decompiled locally |
| U7PG2 firmware 6.8.2+15592 (`BZ.qca956x.v6.8.2+15592.260126.1358`) | `https://fw-download.ubnt.com/data/unifi-firmware/259f-U7PG2-6.8.2-5464c424-a775-4715-8bbc-d84602f55445.bin`, sha256 `3cf8ebe7...7474e` |
| U7HD firmware 6.8.2+15592 (`BZ.ipq806x.v6.8.2+15592.260126.1359`, same build number) | `https://fw-download.ubnt.com/data/unifi-firmware/2d2a-U7HD-6.8.2-20124be7-84e0-4e5c-8ec5-c6397a80c06a.bin`, sha256 `74188855...20677` |

Why two firmwares: the U7PG2 userland is MIPS16 and no MIPS disassembler was at hand. The U7HD build of the same version is ARM, which was disassembled with Go's vendored `armasm`. The U7PG2 `libubnt.so` exports the same functions (`calc_sta_satisfaction`, `calc_vap_satisfaction`, `calc_radio_satisfaction`, `calc_ap_satisfaction`, `calc_sta_satisfaction_mov`, `calc_satisfaction_mov`, `sat_xretry_penalty_thrs`, `sat_xretry_metric_ranges`) and contains byte-identical constant tables (PHY-rate tables at file offsets 224944 and 225008, drop thresholds at 369524). So "VERIFIED" below means: read in the U7HD 6.8.2 code, with constants confirmed present in the U7PG2 6.8.2 binary. The U7PG2 instruction stream itself was not read.

Addresses below are in the U7HD 6.8.2 `libubnt.so` and `mcad`.

### (a) The formula

#### Per client, each stats cycle (`calc_sta_satisfaction`, libubnt 0x26ae4) - VERIFIED

All 16 sub-score slots start at -1 (not computed). A helper (0x26858) records each sub-score: negative values are dropped, values are capped at 100, the running **minimum** becomes `satisfaction_now`, the bit of the minimum becomes `satisfaction_reason_now`, and every sub-score **<= 79** sets its bit in `anomalies_now`.

Bit order (from `anomalies_bar_chart_keys`, matches the controller's bit map in `com/ubnt/service/devmgr/fjCmwuxWyFDHeILC`): 1 weak_signal, 2 low_phy_rate, 4 high_tcp_packet_loss, 8 high_tcp_latency, 16 high_wifi_retries, 32 poor_stream_eff, 64 sleepy_client, 128 high_wifi_latency, 256 sta_ip_timeout, 512 sta_arp_timeout, 1024 sta_dns_timeout, 2048 high_dns_latency, 4096 high_icmp_rtt, 8192 high_disconnect_count, 16384 no_dhcp_response, 32768 high_wifi_drops.

Sub-scores actually computed in 6.8.2 (bits 1, 4, 32, 64, 128 are never set; there are exactly 20 calls to the helper and none uses those bits):

| Bit | Sub-score | Formula |
|---|---|---|
| 2 | PHY rate | 100 if `tx_rate_mov` == 0 or client frequency < 4800 MHz (so **2.4 GHz clients always get 100**). Else `eff = tx_rate_mov(kbps) * avail / 100`, where `avail = min(100, 100 + cu_self_rx + cu_self_tx - cu_total)` of the radio. Piecewise linear on `eff`: 1-10 Mbps -> 60-80; 10-15 -> 80-90; 15-25 -> 90-95; 25-150 Mbps -> 95-100 (freq > 5000 MHz; for 4800-5000 MHz the last segment is 25-30 Mbps); above the top -> 100; **below 1 Mbps -> 0**. |
| 8 | TCP latency | `lat = rx_tcp_stats latency + tx_tcp_stats latency` (ms). 100 if there are no TCP samples or lat <= 99; else `100 - (lat - 100) / 15` (integer). A negative result is discarded (treated as not computed). |
| 16 | Wi-Fi retries | Only when the radio's chip-type enum is 17, 18 or 23 (else 100). Needs `wifi_tx_attempts_mov` >= 30 (else 100). `pct = int(100 * tx_retries_mov / wifi_tx_attempts_mov)`. 100 if pct <= 25; else `100 - 51 * (min(pct,105) - 25) / 100` (integer, truncated). So 50 % -> 88, 67 % -> 79 (anomaly), 100 % -> 62. Retries > attempts -> 0. |
| 32768 | Wi-Fi drops | Only for chip types in `sat_xretry_penalty_thrs` = {23: 0.6, 17: 0.16} (else 100). `pct = 100 * wifi_tx_dropped_mov / wifi_tx_attempts_mov`; **70 if pct >= threshold, else 100**. Drops > attempts -> 0. |
| 256 | IP timeout | 100 if sta field +1216 is non-zero, else 0. |
| 16384 | No DHCP response | 100 if field +1216 non-zero; otherwise 0 or 100 from DHCP counters at +1244..+1250. |
| 512 | ARP timeout | 100 if field +1220 non-zero, else 0. |
| 1024 | DNS timeout | `max(0, 100 - 10 * count)` (field +1224). |
| 2048 | DNS latency | 100 if `dns_avg_latency` == -1; else `100 - 40 * min(lat,1500) / 1500` (100 down to 60). |
| 4096 | ICMP RTT | 100 if rtt <= 99; else `max(0, 100 - (rtt - 100) / 15)`. |
| 8192 | Disconnects | `max(0, 100 - 10 * max(0, count - 1))` (field +1492). |

Field names for `tx_rate_mov` (+376), `wifi_tx_attempts_mov` (+328), `tx_retries_mov` (+336), `wifi_tx_dropped_mov` (+344), `dns_avg_latency` (+1228), `skip_satisfaction` (+516) are VERIFIED from mcad's JSON writer (0x36a38-0x37100). Frequency at +48 is copied from offset 2 of the driver's station record (`isi_freq`): INFERRED from the QCA struct layout. The meaning of +1216/+1220 as "has IP / ARP ok" flags is INFERRED from the 0/100 logic.

#### Smoothing (`calc_sta_satisfaction_mov`, libubnt 0x273d4) - VERIFIED

For each of the 16 sub-scores: mean of the current value and up to 4 history slots, ignoring -1. For bits 256/512/1024 a current 0 gives 0 immediately and historical zeros are ignored. Then again: `satisfaction_real` = **minimum** of the averaged sub-scores, `satisfaction_reason` = its bit (if < 100), `anomalies` = bits with average <= 79.

#### What is reported (mcad JSON writer) - VERIFIED

- `satisfaction_now` = unsmoothed minimum; `satisfaction_real` = smoothed minimum.
- **`satisfaction` = `satisfaction_real` clamped to [60, 100]**, or the negative value (-1) unchanged. Same clamp for sta (mcad 0x36f98), VAP (0x37b64), radio `athstats.satisfaction` (0x35838) and top level (0x3a018).
- Clients with `skip_satisfaction` set get -1 and are left out of the VAP sum.

#### Aggregation - VERIFIED

- VAP (`calc_satisfaction_mov`, 0x27528): sum of clients' `satisfaction_real` / `num_satisfaction_sta`, capped at 100; -1 if no clients. Also builds `anomalies_bar_chart` (count of clients per anomaly bit).
- Radio: sum(vap_score * vap_clients) / sum(vap_clients) over VAPs on that radio; -1 if none.
- AP top level: the same over all VAPs.

#### Controller side - VERIFIED (no formula there)

- Reads only: `com/ubnt/service/devmgr/gliBtHTTrWRd` lines 1042 (top level), 1364 (`athstats.satisfaction`, default -1), 1272 (drops `satisfaction_real` from VAP rows), 2163 (per-sta fields copied).
- `com/ubnt/service/devmgr/QNAsAi` lines 765-767: `satisfaction_avg` = running {total, count} of the client's `satisfaction`.
- `com/ubnt/service/system/xFcFXmUmQb` lines 164-165: `ratio_sta_low_ux_avg` = share of clients with `satisfaction_real` (fallback `satisfaction`) < 70; `ratio_sta_high_ux_avg` = share >= 80; lines 655-665: site/radio averages weighted by `num_sta`, skipping -1.
- `com/ubnt/service/wifi/g/sITFkDkyKy` lines 88-103: site health from the average: >= 95 excellent, >= 50 good, else fair.
- `com/ubnt/service/l/e/ufLrTYaZrJQDCDtqYW`: API `wifi_experience_score` = sta `satisfaction`.
- Wired clients and switch ports also just read `satisfaction` / `satisfaction_reason` (`com/ubnt/service/e/oViVvFSgD` line 136, `com/ubnt/service/e/a/a/d/aoUDgXMlCqEvTSIrOtq`).

#### Web - nothing reliable

Two searches found no Ubiquiti page or community write-up with the formula. The help-center "WiFi Experience Score (WES)" text only covers user feedback reporting (https://help.ui.com/hc/en-us/articles/17169852687639). A third-party page with weights (https://docs.zequenze.com/books/control-users-guide/page/understanding-your-wifi-device-score) is about a different product.

### (b) What this means for unifi-openwrt

- A signal-based estimate does not match: signal is not an input in 6.8.2, and the reported value never goes below 60. `internal/openwrt/satisfaction.go` implements the PHY-rate and retry sub-scores, the 5-cycle smoothing and the clamp.
- A faithful version on OpenWrt: compute the PHY-rate, retry and (optionally) TCP-latency sub-scores as above, take the minimum, average each sub-score over the last 5 cycles, report `satisfaction = clamp(min, 60, 100)`, and send `satisfaction_real` / `satisfaction_now` unclamped. Set `anomalies` bits for sub-scores <= 79.
- Whether the retry and drop sub-scores apply on a real U7PG2 is open (chip-type gate, see (d)); leaving them at 100 is the safe reading for the other chip types.

### (c) cu_total / cu_self_rx / cu_self_tx / cu_interf

- VERIFIED: userland does not compute them. `get_athstat` (libubnt 0x2ef74) does `ioctl(sock, 0x89F9, ifreq{radio name, ptr})` into a 104-byte struct; `cu_total`, `cu_self_rx`, `cu_self_tx`, `cu_interf` are at offsets 48/52/56/60, `noise_floor` at 64. mcad writes them to `athstats` unchanged and logs "invalid stat cu_...: %d" if a value is > 100 (mcad 0x34b28-0x34c44).
- VERIFIED: the only userland use is `avail = min(100, 100 + cu_self_rx + cu_self_tx - cu_total)` in the PHY-rate sub-score (libubnt 0x2a198-0x2a1bc).
- INFERRED (strings in U7PG2 kernel modules only, code not read): the driver derives them from the MAC cycle counters. `ath_hal.ko` has `ar9300_get_mib_cycle_counts_pct` and "BB busy times: rx_clear=%d%%, rx_frame=%d%%, tx_frame=%d%%"; `umac.ko` has `chanutil_total`, `chanutil_selfrx`, `chanutil_selftx` and "rxclear_pct:%d,rxframe_pct:%d,txframe_pct:%d". Likely mapping: total = rx_clear %, self_tx = tx_frame %, self_rx = rx_frame %, interf = the remainder. On OpenWrt the equivalents are the survey counters (busy / tx / rx time over active time).

### (d) Not determined

- The chip-type enum values 17, 18, 23 that gate the retry and drop sub-scores: not mapped to hardware, so it is unknown whether they apply on a U7PG2 (QCA956x + QCA988x).
- How often a sample is taken and when the 4-slot history is rotated (the interval behind the "mov" averaging).
- The exact semantics of the DHCP/ARP/IP flag fields (+1216, +1220, +1244..+1250) and the TCP-stat field names.
- The `satisfaction_exp` variant (second score block, uses `sat_xretry_metric_ranges`, whose range bounds are zero in the file and presumably set at runtime): not followed further.
- Whether earlier 6.x builds (or the 4.x/5.x firmware that introduced the score) used signal, CCQ or other inputs; only 6.8.2 was read. The unused bit names (weak_signal, sleepy_client, poor_stream_eff, high_wifi_latency, high_tcp_packet_loss) suggest older or other-platform versions did.
- The driver-side cu_* arithmetic (kernel modules not disassembled).

## Models and capability profiles

The daemon reports itself as one UniFi AP model (UCI `option model`, flag
`-model`; default `U7PG2`, UniFi AP-AC-Pro). Code: `internal/models`
(catalogue, profiles, suggestion), `internal/inform/profile.go`.

### Catalogue

`internal/models/catalog.json` lists 68 UniFi AP models: code, display name,
firmware version, radios (band, streams, transmit power range) and Wi-Fi
generation. It is generated from unifi-emu's `model_profiles.json` (MIT; see
`NOTICE`), which was harvested from the hardware database of UniFi Network
10.4.57:

```
python3 -I tools/gen-models.py <path to a unifi-emu checkout>
```

`gen-models.py` derives the generation from the display name, gives
codename-only models generation 0 (selectable, never suggested) and applies
measured corrections (the UAP-AC-Pro has 3x3 radios; the source says 2).

`uow -models` lists the catalogue. `uow -ap local -suggest` ranks it for the
AP it runs on: same set of bands first, then the same Wi-Fi generation, then
the same stream count; `U7PG2` wins ties because its capability words are
measured. 6 GHz radios are band `6e` (frequency above 5925 MHz; UCI band
`6g`).

### Capability words

The controller decides what to push from capability words in the inform:

- `fw_caps`, `fw2_caps`: firmware features (for example `0x1000000`
  encrypted syslog; `0x400` SHA-512 SSH password; `0x100000` RADIUS
  dynamic authorization).
- `wifi_caps`, `wifi_caps2`: wireless features. Without `wifi_caps` every
  wireless feature counts as unsupported. The UAP-AC-Pro value
  `559857373` sets VWIRE, BANDSTEER (+PER_VAP), RF_SCAN, BGA_FILTER, MESH,
  MINRATE_CTRL, MULTIPLE_ACL_LIST, MESHV3, RADIUS_MAC_AUTH, OPEN_HOSTAPD,
  ELEMENT_ADOPT, WPA3, FILS, QOS_MARK_BITS, WPA_PSK_FILE, LOCK_AP,
  QCA_VAP_NOMENCLATURE_V2 and bit 22; `wifi_caps2 48` = ACS_DFS,
  ROAMING_ASSISTANT.
- Per radio: `ieee_modes`, `radio_caps`, `radio_caps2`, `is_11ac`, `has_dfs`,
  `has_fccdfs`. The 5 GHz width resolver uses min(country, radio ht, device
  caps), and the device caps allow 80 MHz only if the radio row has `is_11ac`
  (or `is_11ax`).
- `hw_caps`: LED bar colour (2) and brightness (16384), see `docs/led.md`.

With nothing claimed (`fw_caps 3`, no `wifi_caps`) the controller pushes a
U7PG2 WPA2 only and 40 MHz on 5 GHz. Changing capability words alone does not
trigger a new provisioning; the next settings change does.

The only full set of words is the UAP-AC-Pro one
(`internal/inform/profile.go`, from a real U7PG2 on 6.8.2.15592 as recorded
by open-unifi). `-profile-caps` (UCI `profile_caps`) claims it; the
translator then clamps what the hardware or the AP's hostapd cannot do. For
any other model, `models.Profile` takes identity, firmware version and radios
from the catalogue and the capability words from the AC-Pro profile.

### Adding a model or profile

1. If the model is missing from `catalog.json`, add it to the source data
   and regenerate, or add it by hand with the same fields.
2. If you have the model's real capability words (from an inform of a real
   device), add a `Profile` next to `inform.U7PG2` and return it from
   `models.Profile` for that code, the way U7PG2 is handled.
3. Run `go test ./internal/models/ ./internal/inform/` and check
   `uow -ap local -suggest` on the AP.
4. Adopt it and check that the controller lists the right number of radios
   **before** changing anything: the controller never forgets a radio row
   once reported (only Remove and adopt again clears it).

Whether a given model is accepted depends on the controller version: Network
10.4 no longer lists the first-generation UAP-AC (`U7E`) and AC Outdoor
(`U7O`), for example. Only `U7PG2` on Network 10.6 has been tested.

## Status LED

Code: `internal/openwrt/led.go` (states and renderer), `internal/openwrt/actions.go`.

### What a UniFi AP shows

| State | LED |
|---|---|
| starting, controller not answering yet | flashing white |
| factory default / waiting for adoption | steady white |
| busy (upgrading) | alternating white and blue |
| adopted and running | steady blue |
| Locate | fast blue flashing (also while the LED is set to off) |
| LED off | dark |

### What the controller sends

- On/off: the device's `led_override` (`default`, `on`, `off`) combined with
  the site's LED setting arrives as `mgmt_cfg` `led_enabled=true|false`.
- Locate: `cmd set-locate` / `unset-locate`. With a speaker and `alsa-utils`
  the daemon also beeps (UCI `locate_beep`).
- Colour and brightness: only via system_cfg `ledbar.*`
  (`status`, `persistent`, `brightness` 0-255, `color.1.{r,g,b}`), and only to
  devices that report `hw_caps` bit 2 (LED bar colour) or 16384 (PWM
  brightness). A UAP-AC-Pro reports 0. Claim the bits with UCI `hw_caps` (or
  `-hw-caps`) if the AP's LEDs can show colour or brightness.

The controller's model database has no LED data: each device declares
`hw_caps` itself.

### Rendering on OpenWrt

The daemon picks the status LEDs from `/sys/class/leds` (names with
`status`, else `power`, else `system`, else all) and maps the state onto
them:

- RGB groups (for example a ring of red/green/blue LEDs): the colour is mixed
  from the channels, scaled by the brightness.
- Single-colour white and blue LEDs: white and blue states use them.
- Otherwise whatever LED is there stands in.

Blinking uses the kernel `timer` trigger. The state is re-applied when the
daemon starts and after the AP reboots.

UCI `led_idle 'rainbow'` replaces steady blue with a slow colour cycle on RGB
LED rings. Empty (the default) behaves like the UniFi model.

## Mesh (wireless uplink)

An AP adopted with UCI `mesh_uplink '1'` joins the site's UniFi mesh as a
wireless uplink when it has no cable, like a UniFi AP that loses its wire.
Tested: an OpenWrt AP meshing to a UniFi console's AP, shown as "Meshing to"
the parent in the UniFi. Code: `internal/openwrt/mesh.go`,
`internal/openwrt/unifi.go` (`uplinkVAP`).

### What the controller sends

With site meshing on (`connectivity.enabled`), system_cfg carries
`mesh.status=enabled`, `mesh.version=3`, `mesh.essid=vwire-<hex>` and a
hidden VAP (`vwireap<N>`, `usage=downlink`, `wds=enabled`, `vwire=enabled`)
whose `aaa.N` holds WPA2-PSK/CCMP and the mesh passphrase. The translator does
not create that VAP: serving mesh children is not built.

### The watcher

The daemon writes `/etc/unifi-openwrt/mesh.conf` (mode 600: SSID,
passphrase, 5 GHz radio, accepted parents) and `mesh.sh`; procd runs
`mesh.sh` as its own instance, so it keeps working if the daemon stops.

- Every second: wired = any non-Wi-Fi `br-lan` member with carrier.
- Wired: drop the wireless uplink (link down first, then delete
  `wireless.unifi_mesh`, `wifi reload`).
- 20 s without a cable: add `wireless.unifi_mesh` (mode `sta`, `wds 1`,
  `psk2+ccmp`, network `lan`) and `wifi reload`.

**The AP is never uplinked by cable and Wi-Fi at once** (that would loop the
network), except for at most about a second after a cable comes back.

A UniFi parent accepts a 4-address client that is not Ubiquiti firmware.
On a single-channel driver (ath10k) the AP's own 5 GHz service moves to the
parent's channel while meshed; 5 GHz clients drop once at the join and once
when the cable returns.

### How the controller learns the wireless uplink

From the decompiled controller (`devmgr.C.aoUDgXMlCqEvTSIrOtq`): for the
inform VAP whose `name` equals the inform's `uplink`, with `usage: "uplink"`,
`channel` != 0 and `up`, each station in its `sta_table` with
`rx_packets` > 0 is resolved to the parent device: the station's
`serialno` if it is a MAC, else the ESSID `(vwire|vport)-<12 hex>`. The
site mesh ESSID has 16 hex digits, so `serialno` is needed. The daemon sends
the parent BSSID as the one station and, as `serialno`, the device on the LAN
whose MAC shares bytes 2-5 with that BSSID (UniFi BSSIDs keep the device
MAC's middle four bytes). The controller then sets `uplink.type=wireless`,
`uplink_ap_mac`, `is_mesh_v3`, signal and rates.

The label can stay "wire" while the controller still holds a fresher
topology entry for the AP from the switch that saw it wired
(`devmgr.o.mMObEXELV` merges stored LLDP/legacy entries over the device's
own uplink). Once the switch reports the port down, the next meshed inform
shows "wireless".

After the cable is back, the `eth0` row carries `signal 0` so the stored
uplink loses its mesh values (see `docs/stats.md`).

### Mesh Parent

The device's `mesh_uplink_1` / `mesh_uplink_2` arrive as `mesh.serial1/2`.
The watcher sets `bssid_whitelist` (wpa_supplicant `bssid_accept`)
`00:<bytes 2-5 of the parent MAC>:00/00:ff:ff:ff:ff:00` per parent, so the
AP joins only that parent's BSSIDs; empty = Auto. Built and unit-tested,
**not tested live** (the UniFi offers Mesh Parent only for a meshed AP).

### Not built

- Serving mesh children (the downlink `vwire` VAP).
- VLANs over the mesh uplink (`vwireap<N>.<vid>` bridge ports).

## VLANs

A WLAN on a tagged UniFi network (a VLAN ID set on the network) works: its
clients get addresses on that VLAN. Code: `internal/translate/vlan.go`.

### What the controller sends

For a WLAN on VLAN `<vid>`:

- `vlan.N.devname` / `vlan.N.id` for each interface that carries the VLAN
  (`eth0`, `eth1`, the element-adopt VAP, the mesh VAP).
- A second bridge `bridge.2.devname=br0.<vid>` whose ports are the WLAN's
  VAPs (`wifi0ap5`, `wifi1ap6`, ...) and `eth0.<vid>`, `eth1.<vid>`, ...
- `netconf.N.devname=br0.<vid>` with `ip=0.0.0.0`: the AP has no address on
  the VLAN.

A VAP's own `wireless.N` block has no VLAN key: membership is the bridge
port list.

### OpenWrt

- `network.uowvlan<vid>`: a bridge device `br-uow<vid>` with port
  `<first br-lan port>.<vid>` (an 802.1Q device on the cable port). `br-lan`
  is not changed: a VLAN device on a DSA port in a bridge without VLAN
  filtering receives the tagged frames.
- `network.uow<vid>`: interface on that bridge, `proto none`: no address and
  no DHCP, DNS or RA from the AP.
- The WLAN's wifi-ifaces get `network uow<vid>`.
- `uow*` sections are deleted again when the VLAN goes away.

### Not built

- VLAN on the mesh uplink (`vwireap<N>.<vid>`).
- VLAN on a secondary cable port (`eth1.<vid>`).
- Per-client VLANs from RADIUS.

## Guest WLANs and the guest portal

Code: `internal/translate/guest.go`, `internal/openwrt/guest.go`,
`internal/device/portal.go`.

### Isolation

For a WLAN with "guest network" on, system_cfg marks its VAPs
(`aaa.N.is_guest=true`) and sends what a UAP runs: about 46 `ebtables.N.cmd`
lines (GUESTIN / GUESTOUT chains: EAPOL, ARP and DHCP allowed; DNS
redirected to the AP; `guest_pre_allow` allowed; IPv6, broadcast and
`guest_restricted` dropped; unauthorized guests' web traffic redirected),
a few `iptables.N.cmd` lines, `ipset` (`guest_restricted` = 224.0.0.0/4 and
the private ranges, `guest_pre_allow`, `guest_authorized_mac`) and
`redirector` (the portal URL).

unifi-openwrt mirrors this with an nftables **bridge** table `unifi_guest`
on the guest interfaces (needs `kmod-nft-bridge`): EAPOL/ARP/DHCP pass, DNS
goes to a separate DNS-only dnsmasq on the AP (port 5300, nothing listens on
53, so the LAN sees no new DNS server), pre-allowed ranges pass, IPv6 and
restricted ranges are dropped, the internet passes. Runtime only,
re-applied after each sync and reboot, removed when no WLAN is a guest WLAN.

### Portal

With the site's guest portal on, unauthorized guests may only reach DHCP,
DNS and the controller's portal ports (8880, 8843, 8881, 8882); their port 80
is redirected to a small redirector on the AP that sends them to
`redirector.url?id=<mac>&ap=<ap mac>&t=<unix>&url=<orig>&ssid=<ssid>`.

After a successful portal login the controller does **not** send
`authorize-guest` (that command is only for re-authorization and expiry;
`devmgr.taPwkNVbOeTvvcxIise`). It redirects the browser to
`http://<host>:8882/redirect?ec=<cookie>`, where the host is the console's
address, not the AP's. So the AP intercepts guests' TCP 8882 whatever the
destination and answers it on its own `:8882` (`-portal-listen`).

The cookie: the guest context as JSON plus `\n` (`mac`, `ap`, `url`, `ssid`,
`t`, `expire` in Unix seconds, `kicks`, ...), AES-128-CBC with PKCS#5
padding, key = IV = the hex-decoded **`guest_token`**, base64url without
padding. The AP makes up the token (32 upper-case hex digits, kept in its
state as a secret) and reports it with `guest_kicks` in every inform; the
controller copies both to the device record. Without the token the login
can never reach the AP.

The AP decrypts the cookie, checks `ap` is its own MAC and that it has not
expired, adds the client to the nftables `authorized` set with the remaining
time as timeout, and redirects to `url`. A forged cookie gets 403.

## RF scan and 802.11k neighbour list

Code: `internal/openwrt/scan.go`, `internal/openwrt/rrm.go`, `cmd/m2/scan.go`.

### What the controller reads

From the decompiled controller (`UQKIrjV`, `DRUVu`, `gliBtHTTrWRd`):

- `radio_table[i].scan_table`: `bssid`, `essid`, `channel`, `bw`, `freq`,
  `center_freq`, `rssi` (signal + 95), `signal`, `security` (`open`, `wep`,
  `wpapsk`, `wpaeap`), `age` (seconds), `band`, `is_unifi`, `serialno`.
  Old rows are dropped. Non-UniFi rows younger than 30 s feed the rogue AP
  list (`stat/rogueap`); **a non-UniFi row whose ESSID is one of the site's
  WLANs raises a rogue-AP alarm**. `is_unifi` + `serialno` (device MAC)
  mark a sibling. So the daemon leaves out its own BSSIDs and site-SSID rows
  it cannot match to a LAN device (BSSID bytes 2-5 = a LAN device's MAC
  bytes 2-5).
- `radio_table[i].spectrum_table`: `[{channel, width, center_freq,
  utilization, interference}]`, with `spectrum_table_time` = age in
  seconds. Served by `stat/spectrum-scan/<mac>`. Top-level
  `spectrum_scanning` is true while scanning.

### Scanning on OpenWrt

`iw dev X survey dump; iw dev X scan ap-force; iw dev X survey dump` per
radio, a few channels per step so clients get the radio back in between.
Utilization = busy / active per 20 MHz channel (the in-use channel's counters
are cumulative, so the difference is used); 40/80 MHz blocks when all their
channels were seen. Results are taken from each step's output (the kernel
drops BSS entries after about 30 s).

- `cmd scan` / `spectrum-scan` from the controller scan every radio.
- A background scan every 15 minutes runs only on radios with no clients and
  no mesh uplink: going off-channel, even briefly, makes clients leave.
- A **spare radio** (a phy with no interfaces in UCI) is used first: the
  daemon adds a listen-only interface `uowscan0` there, with no UCI change
  and no Wi-Fi reload, and the collector hides it from the controller.
- An RF scan on a radio with clients disconnects them, as on a real UAP.

ath10k limits seen: a full-band scan from an AP-mode 5 GHz interface may only
find the own channel (hence the small steps); the 2.4 GHz radio reported
survey data only for channel 1 and the channel in use.

### 802.11k neighbour list

UniFi has no central steering: each AP applies what system_cfg says. With a
WLAN's 802.11k on (`aaa.N.11k.status=enabled` -> `ieee80211k 1`), hostapd's
neighbour list still holds only itself. The daemon adds sibling BSSes from
its scans (same SSID, matched to a LAN device as above, remembered for an
hour) and its own BSS of the same SSID on the other band, with
`ubus call hostapd.<if> rrm_nr_set` (own entry from `rrm_nr_get_own` first),
every 5 minutes and right after each Wi-Fi reload (hostapd forgets the list
on restart). Element: BSSID, BSSID info `0x08af` (+`0x1000` VHT on 5 GHz),
the 20 MHz operating class (81, 115, 118, 121, 125), channel, PHY type 7/9.
