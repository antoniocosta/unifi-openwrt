// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"testing"
	"time"
)

// Shaped like the output of an AirPort Time Capsule A1470 (7.9.1) in bridge
// mode with a 2.4 GHz WLAN and a client VAP on 5 GHz; addresses made up.
const airportSample = `=== sys
release=6.0
hostname=airport-test
now=1000154
boottime=1000000
physmem=268435456
loadavg=0.23 0.08 0.03
cp_time=user = 89, nice = 0, sys = 211, intr = 368, idle = 14863
model=syAM=TimeCapsule8,119
=== vmstat
     4096 bytes per page
    50905 pages free
      244 cached file pages
     1168 cached executable pages
=== lan
bridge0: flags=ffffe043<UP,BROADCAST,RUNNING,LINK1,LINK2,MULTICAST> metric 0 mtu 1500
	ether 02:aa:00:00:00:01
	inet6 fe80::aa:ff:fe00:1%bridge0 prefixlen 64 scopeid 0x7
	inet 192.0.2.142 netmask 0xfffffe00 broadcast 192.0.3.255
	inet 169.254.220.19 netmask 0xffff0000 broadcast 169.254.255.255
=== route
Internet:
Destination        Gateway            Flags    Refs      Use    Mtu Interface
default            192.0.2.1          UGS         2        1      -  bridge0
=== arp
? (192.0.2.1) at 2:bb:0:0:0:1 on bridge0
=== fdb
bridge0: flags=ffffe043<UP,BROADCAST,RUNNING,LINK1,LINK2,MULTICAST>
	Interfaces:
		wlan9 flags=3<LEARNING,DISCOVER>
			port 8 priority 128
		wlan0 flags=3<LEARNING,DISCOVER>
			port 6 priority 128
		bcmeth0 flags=3<LEARNING,DISCOVER>
			port 2 priority 128
	Address cache (max cache: 100, timeout: 1200):
		02:bb:00:00:00:01 wlan9 1103 flags=0<>
		02:cc:00:00:00:01 bcmeth0 1056 flags=0<>
=== phy
	 	 PHY (0x20A480E0) index 0
	 	 	 Link State - Current  (link state is Down)
	 	 PHY (0x20A48110) index 2
	 	 	 Link State - Current  (link state 1000Mbps Full Duplex)
=== netstat
Name  Mtu   Network       Address               Ibytes     Obytes
bcmet 1500  <Link>        02:aa:00:00:00:01          0          0
bcmet 1500  <Link>        02:aa:00:00:00:01    6617597    2071056
wlan0 1500  <Link>        02:aa:00:00:00:02         10    1340732
wlan9 1500  <Link>        02:aa:00:00:00:03    1927679    6894017
wlan9 1500  fe80::/64     fe80::aa:ff:fe00:3    1927679    6894017
=== netpkts
Name  Mtu   Network       Address              Ipkts Ierrs    Opkts Oerrs Colls
bcmet 1500  <Link>        02:aa:00:00:00:01      896     0      930     0     0
wlan0 1500  <Link>        02:aa:00:00:00:02       12     1      817     2     0
wlan9 1500  <Link>        02:aa:00:00:00:03      956     0      751     0     0
=== iface wlan0
wlan0: flags=ffffe963<UP,BROADCAST,NOTRAILERS,RUNNING,PROMISC,SIMPLEX,LINK1,LINK2,MULTICAST> metric 0 mtu 1500
	ether 02:aa:00:00:00:02
	ssid "Home Net" channel 6 (2437 Mhz 11g ht/20) bssid 02:aa:00:00:00:02
	authmode WPA2/802.11i privacy OFF deftxkey 2 txpower 24 dotd
	media: autoselect hostap
	status: running
=== sta wlan0
ADDR               AID CHAN TXRATE RXRATE  RSSI IDLE  TXSEQ  RXSEQ CAPS FLAG
02:dd:00:00:00:01    1    6    72M    65M -48.0    2      0      0      E
=== noise wlan0
-87
=== counters wlan0
txframe 17640 txbyte 2896189 txretrans 41 txerror 25 rxframe 16939 rxbyte 2524696 rxerror 3
txprshort 0 txdmawar 0 txnobuf 2 txnoassoc 10 txchit 61 txcmiss 17586
reset 7 txserr 96 txphyerr 0 txphycrs 0 txfail 1 tbtt 13237
rxcrc 273844 rxnobuf 4 rxnondata 0
txphyerr 9
=== assoc wlan0
--- 02:DD:00:00:00:01
 STA 02:DD:00:00:00:01:
	 idle 2 seconds
	 in network 61 seconds
	 state: AUTHENTICATED ASSOCIATED AUTHORIZED
	 flags 0x1e03b: BRCM WME N_CAP AMPDU
	 tx data pkts: 40
	 tx data bytes: 5000
	 tx failures: 1
	 rx data pkts: 50
	 rx data bytes: 7000
	 rate of last tx pkt: 72222 kbps
	 rate of last rx pkt: 65000 kbps
=== iface wlan9
wlan9: flags=ffff8963<UP,BROADCAST,NOTRAILERS,RUNNING,PROMISC,SIMPLEX,MULTICAST> metric 0 mtu 1500
	ether 02:aa:00:00:00:03
	ssid vwire-0123456789abcdef channel 40 (5200 Mhz 11a vht/80) bssid 02:bb:00:00:00:02
	authmode WPA2/802.11i privacy OFF txpower 31.5 dotd rtsthreshold 2347
	media: manual
	status: associated
=== sta wlan9
ADDR               AID CHAN TXRATE RXRATE  RSSI IDLE  TXSEQ  RXSEQ CAPS FLAG
02:bb:00:00:00:02    0   40   866M     6M -23.0    0      0      0      QEHT (rssi -27:-23:-23 nf -92:-92:-92)
=== noise wlan9
-92
=== assoc wlan9
--- 02:BB:00:00:00:02
 STA 02:BB:00:00:00:02:
	 idle 0 seconds
	 in network 103 seconds
	 state: AUTHENTICATED ASSOCIATED
	 flags 0x10e01a: WME N_CAP VHT_CAP AMPDU
	 tx data bytes: 3392849
	 rx data bytes: 1379700
	 rate of last tx pkt: 866667 kbps
	 rate of last rx pkt: 6000 kbps
=== iface wlan1
wlan1: flags=ffffe822<BROADCAST,NOTRAILERS,SIMPLEX,LINK1,LINK2,MULTICAST> metric 0 mtu 1500
	ssid "" channel 34 (5170 Mhz 11a ht/20)
	media: none (autoselect hostap)
	status: no carrier
=== sta wlan1
=== noise wlan1
=== assoc wlan1
`

func TestParseAirPort(t *testing.T) {
	s, err := ParseAirPort([]byte(airportSample), time.Unix(1000200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if s.Model != "Apple TimeCapsule8,119" || s.Uptime != 154 || s.MAC != "02:aa:00:00:00:01" {
		t.Errorf("model %q uptime %d mac %q", s.Model, s.Uptime, s.MAC)
	}
	if s.IP != "192.0.2.142" || s.Netmask != "255.255.254.0" || s.Gateway != "192.0.2.1" {
		t.Errorf("ip %q mask %q gw %q", s.IP, s.Netmask, s.Gateway)
	}
	if s.ARP["02:bb:00:00:00:01"] != "192.0.2.1" {
		t.Errorf("arp %v (a MAC printed without leading zeros must be padded)", s.ARP)
	}
	if s.CPUTotal != 15531 || s.CPUIdle != 14863 || s.MemFree != 50905*4096 {
		t.Errorf("cpu %d/%d memfree %d", s.CPUIdle, s.CPUTotal, s.MemFree)
	}
	if len(s.Radios) != 2 || s.Radios[0].Band != "ng" || s.Radios[1].Band != "na" {
		t.Fatalf("radios: %+v (the VAP that is down must be left out)", s.Radios)
	}
	ap := s.Radios[0].VAPs[0]
	if ap.Mode != "Master" || ap.SSID != "Home Net" || ap.Channel != 6 || ap.Width != 20 || ap.Stats["tx_bytes"] != 1340732 {
		t.Errorf("ap vap: %+v", ap)
	}
	if ap.Stats["rx_packets"] != 12 || ap.Stats["tx_packets"] != 817 || ap.Stats["tx_errors"] != 25 {
		t.Errorf("ap packets: %v", ap.Stats)
	}
	if ap.Stats["wifi_tx"] != 17640 || ap.Stats["wifi_retries"] != 41 || ap.Stats["wifi_failed"] != 25 ||
		ap.Stats["tx_dropped"] != 3 || ap.Stats["rx_dropped"] != 4 || ap.Stats["rx_errors"] != 3 {
		t.Errorf("radio counters: %v", ap.Stats)
	}
	if len(ap.Stations) != 1 {
		t.Fatalf("stations: %+v", ap.Stations)
	}
	st := ap.Stations[0]
	if st.MAC != "02:dd:00:00:00:01" || st.Signal != -48 || !st.Authorized || !st.HT || st.VHT ||
		st.Connected != 61 || st.Inactive != 2000 || st.TxRate != 72222 || st.RxBytes != 7000 || st.TxFailed != 1 {
		t.Errorf("station: %+v", st)
	}
	up := s.Radios[1].VAPs[0]
	if _, has := up.Stats["wifi_tx"]; has {
		t.Errorf("the uplink VAP must not carry the radio counters: %v", up.Stats)
	}
	if up.Mode != "Client" || up.BSSID != "02:bb:00:00:00:02" || up.Width != 80 || !up.VHT || up.TxPower != 31 {
		t.Errorf("uplink vap: %+v", up)
	}
	if len(up.Stations) != 1 || up.Stations[0].Signal != -23 || !up.Stations[0].Authorized || up.Stations[0].TxRate != 866667 {
		t.Errorf("uplink parent: %+v", up.Stations)
	}
	var wan, lan *Port
	for i := range s.Ports {
		switch s.Ports[i].Name {
		case "wan":
			wan = &s.Ports[i]
		case "lan2":
			lan = &s.Ports[i]
		}
	}
	if wan == nil || wan.Up || lan == nil || !lan.Up || lan.Speed != 1000 || lan.BridgePort != 2 || lan.RxBytes != 6617597 {
		t.Errorf("ports: %+v", s.Ports)
	}
	var gw *FDBEntry
	for i := range s.FDB {
		if s.FDB[i].MAC == "02:bb:00:00:00:01" {
			gw = &s.FDB[i]
		}
	}
	if gw == nil || gw.Port != 8 || gw.Age != 97 {
		t.Errorf("fdb: %+v", s.FDB)
	}
}
