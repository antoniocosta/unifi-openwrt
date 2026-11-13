// SPDX-License-Identifier: GPL-2.0-or-later

// uow is the unifi-openwrt daemon: it makes an OpenWrt access point adopt
// into a UniFi controller as a UniFi AP model (-model, default U7PG2), reports
// the AP's radios, clients and stats in the model's inform format, and
// applies the controller's settings to OpenWrt (-apply; log-only otherwise).
// It normally runs on the AP itself (-ap local, see /etc/init.d/unifi-openwrt);
// without -ap it runs a made-up device for protocol testing.
//
// Safety:
//   - every apply is checked and rolled back automatically if the Wi-Fi does
//     not come up or the daemon cannot confirm it;
//   - Ubiquiti firmware upgrades are answered but never downloaded or applied;
//   - a made-up device uses only a locally administered MAC (02:...);
//   - optional read-only UniFi health check (GET) every -health-every;
//   - stop after -max-errors consecutive bad answers;
//   - keys are never logged; system_cfg is never logged (it holds the
//     Wi-Fi passphrase).
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"github.com/antoniocosta/unifi-openwrt/internal/models"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/antoniocosta/unifi-openwrt/internal/device"
	"github.com/antoniocosta/unifi-openwrt/internal/inform"
	"github.com/antoniocosta/unifi-openwrt/internal/openwrt"
	"github.com/antoniocosta/unifi-openwrt/internal/translate"
	"github.com/antoniocosta/unifi-openwrt/internal/unifiapi"
)

func main() {
	informURL := flag.String("url", "http://192.168.1.1:8080/inform", "controller inform URL (IP literal host)")
	console := flag.String("console", "192.168.1.1", "console host for read-only health checks")
	macStr := flag.String("mac", "02:6f:77:72:74:01", "device MAC: made-up (02:...) without -ap; with -ap it must equal the AP's br-lan MAC")
	apAddr := flag.String("ap", "", "real OpenWrt AP to report (ssh host:port); empty = made-up device")
	apUser := flag.String("ap-user", "root", "ssh user on the AP")
	apKey := flag.String("ap-key", "ap_ed25519", "ssh key for the AP")
	apKH := flag.String("ap-known-hosts", "ap_known_hosts", "known_hosts with the AP's key")
	hostname := flag.String("hostname", "OpenWrt", "hostname reported to the controller")
	wifiCaps := flag.Int("wifi-caps", 0, "UNIFI_WIFI_CAP bitmap to claim (131072 = WPA3); only what the AP can do")
	flag.BoolVar(&meshUplink, "mesh-uplink", false, "join the site's mesh as a wireless uplink when no cable has link (AP-side watcher)")
	flag.IntVar(&hwCaps, "hw-caps", 0, "hw_caps to claim (2 = LED colour, 16384 = LED brightness); 0 = the model's")
	flag.StringVar(&portalListen, "portal-listen", "", "guest portal redirect endpoint (UniFi APs: :8882); empty = off")
	flag.StringVar(&ledIdle, "led-idle", "", "idle LED effect instead of steady blue: \"rainbow\" (RGB LED rings, daemon on the AP); empty = blue like the UniFi model")
	informEvery := flag.Duration("inform-every", 0, "inform at least this often (0 = the controller's interval, 10 s); 2s makes the panel's live bars move")
	modelCode := flag.String("model", "U7PG2", "UniFi model to be adopted as (see -models / -suggest)")
	listModels := flag.Bool("models", false, "list the UniFi AP models and exit")
	suggest := flag.Bool("suggest", false, "suggest the closest UniFi model for the AP (-ap) and exit")
	profileCaps := flag.Bool("profile-caps", false, "report the real U7PG2's capability words (wifi/fw/radio caps); the translator clamps what the AP can't do")
	apply := flag.Bool("apply", false, "apply the controller's wireless settings to the AP (with automatic rollback); default log-only")
	stopOnReset := flag.Bool("stop-on-reset", true, "exit after a setdefault (device removed) instead of going pending again")
	flag.BoolVar(&locateBeepOn, "locate-beep", true, "beep on Locate (APs with a speaker)")
	stateDir := flag.String("state", "uow-state", "state directory (created mode 700)")
	healthEvery := flag.Duration("health-every", time.Minute, "health check period")
	sshListen := flag.String("ssh-listen", ":2222", "fake device SSH (adoption only); empty = off")
	maxErrors := flag.Int("max-errors", 6, "consecutive bad answers before stopping")
	flag.Parse()
	if h, _, err := net.SplitHostPort(*apAddr); err == nil {
		apHost = h
	}
	log.SetFlags(log.Ldate | log.Ltime)
	if *listModels {
		for _, m := range models.All() {
			fmt.Println(m)
		}
		return
	}
	model, ok := models.Lookup(*modelCode)
	if !ok {
		log.Fatalf("unknown UniFi model %q (see -models)", *modelCode)
	}
	profile := models.Profile(model)

	mac, err := net.ParseMAC(*macStr)
	if err != nil || len(mac) != 6 || mac[0]&0x01 != 0 {
		log.Fatalf("bad MAC %q", *macStr)
	}
	var ap *openwrt.Client
	var snap *openwrt.Snapshot
	if *apAddr == "" {
		if mac[0]&0x02 == 0 {
			log.Fatalf("refusing MAC %s: without -ap only a locally administered MAC is allowed", mac)
		}
	} else {
		ap = openwrt.NewClient(openwrt.Target{Addr: *apAddr, User: *apUser, KeyFile: *apKey, KnownHosts: *apKH})
		defer ap.Close()
		if snap, err = ap.Collect(); err != nil {
			log.Fatalf("reading the AP: %v", err)
		}
		if *suggest {
			d := snap.Describe()
			fmt.Printf("this AP: %q, bands %v, %d streams, Wi-Fi %d\nclosest UniFi models:\n", snap.Model, d.Bands, d.NSS, d.Gen)
			for i, m := range models.Suggest(d) {
				if i == 5 {
					break
				}
				fmt.Println("  ", m)
			}
			return
		}
		if !strings.EqualFold(snap.MAC, mac.String()) {
			log.Fatalf("refusing: -mac %s is not the AP's MAC (%s, %q)", mac, snap.MAC, snap.Model)
		}
		log.Printf("AP: %q %s, %s, ip %s, %d radios", snap.Model, snap.Release, snap.MAC, snap.IP, len(snap.Radios))
		if *apAddr == "local" {
			apHost = snap.IP // keeps the static-IP guard on (planNetwork)
		}
	}
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Fatal(err)
	}
	os.Chmod(*stateDir, 0o700)
	statePath := filepath.Join(*stateDir, "state.json")
	st, found, err := device.Load(statePath)
	if err != nil {
		log.Fatal(err)
	}
	if !found {
		st = device.NewState(*informURL)
		st.Version = profile.Version
	}
	myIP, err := localIPToward(st.InformURL)
	if err != nil {
		log.Fatal(err)
	}
	if snap != nil {
		myIP = snap.IP
	}
	x := &device.Session{
		S: st, Boot: time.Now(), StateDir: *stateDir,
		Dev: inform.Device{MAC: mac, IP: myIP, Netmask: "255.255.255.0", Gateway: "192.168.1.1",
			Hostname: *hostname, Model: profile.Model, ModelDisplay: profile.Display},
	}
	guestSession = x
	if x.S.HashID == "" {
		h, err := device.NewHashID()
		if err != nil {
			log.Fatal(err)
		}
		x.S.HashID = h
		if err := x.S.Save(statePath); err != nil {
			log.Fatal(err)
		}
	}
	if *apAddr == "local" { // the daemon runs on the AP: logread is ours
		go syslogForwarder(x, *stateDir)
	}
	if portalListen != "" {
		if x.S.GuestToken == "" {
			t, err := device.NewGuestToken()
			if err != nil {
				log.Fatal(err)
			}
			x.S.GuestToken = t
			if err := x.S.Save(statePath); err != nil {
				log.Fatal(err)
			}
		}
		if ap != nil {
			go portalServer(portalListen, ap, x, mac.String(), statePath)
		}
	}
	x.WifiCaps = *wifiCaps
	x.HWCaps = hwCaps
	var radioCaps map[string]openwrt.RadioCaps
	if *profileCaps {
		p := profile
		x.WifiCaps, x.WifiCaps2, x.FWCaps, x.FW2Caps = int(p.WifiCaps), int(p.WifiCaps2), int(p.FWCaps), int(p.FW2Caps)
		radioCaps = map[string]openwrt.RadioCaps{}
		for b, r := range p.Radios {
			radioCaps[b] = openwrt.RadioCaps{IEEEModes: r.IEEEModes, RadioCaps: r.RadioCaps, RadioCaps2: r.RadioCaps2, HasDFS: r.HasDFS, HasFCCDFS: r.HasFCCDFS}
		}
		log.Printf("profile caps: %s wifi_caps=%d wifi_caps2=%d fw_caps=%d fw2_caps=%d", p.Model, p.WifiCaps, p.WifiCaps2, p.FWCaps, p.FW2Caps)
	}
	if ap != nil {
		prev := snap
		var lastErr string
		x.Extra = func() map[string]any {
			s, err := ap.Collect()
			if err != nil {
				if err.Error() != lastErr {
					log.Printf("reading the AP failed (reporting last values): %v", err)
					lastErr = err.Error()
				}
				s = prev
			} else {
				lastErr = ""
			}
			cfg, _ := os.ReadFile(filepath.Join(*stateDir, "system_cfg.txt"))
			info := openwrt.InfoFromCfg(string(cfg))
			info.Authorized = map[string]bool{}
			for m, end := range x.S.Guests {
				if time.Now().Unix() < end {
					info.Authorized[m] = true
				}
			}
			shapeReconcile(ap, s)
			minRSSIKick(ap, s)
			f := s.Fields(prev, info, radioCaps)
			scans.fields(f, s, info)
			prev = s
			return f
		}
	}
	var adoptReqs chan device.AdoptRequest
	if *sshListen != "" {
		_, port, err := net.SplitHostPort(*sshListen)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Sscan(port, &x.SSHPort)
		allow := []string{*console}
		if snap != nil {
			allow = append(allow, snap.IP) // console SSH forwarded through the AP
		}
		sa := &device.SSHAdopt{Listen: *sshListen, AllowFrom: allow,
			HostKey: filepath.Join(*stateDir, "ssh_host_ed25519.pem"), Requests: make(chan device.AdoptRequest, 4)}
		if err := sa.Start(); err != nil {
			log.Fatalf("ssh-adopt: %v", err)
		}
		defer sa.Close()
		adoptReqs = sa.Requests
		log.Printf("ssh-adopt listening on %s (only from %v), reported as sshd_port=%d", *sshListen, allow, x.SSHPort)
	}
	log.Printf("start: mac=%s ip=%s adopted=%v gcm=%v cfgversion=%s version=%s (state %s)",
		mac, myIP, x.S.Adopted, x.S.GCM, x.S.CfgVersion, x.S.Version, map[bool]string{true: "loaded", false: "new"}[found])

	// Console health checks (development safety net) need the UniFi API key;
	// -health-every 0 runs without them (the daemon on the AP itself must
	// never hold that key). AP-side checks and rollbacks stay.
	var api *unifiapi.Client
	var base unifiapi.Health
	if *healthEvery > 0 {
		key, err := unifiapi.KeyFromSecrets()
		if err != nil {
			log.Fatal(err)
		}
		api = unifiapi.New(*console, "default", key)
		base, err = api.Health()
		if err != nil {
			log.Fatalf("health check failed, not starting: %v", err)
		}
		log.Printf("health: %s", fmtHealth(base))
	} else {
		log.Printf("console health checks off (-health-every 0)")
	}
	lastHealth := time.Now()
	x.RestoreFromMgmtCfg()
	ledCfgPath = filepath.Join(*stateDir, "system_cfg.txt")
	if ap != nil {
		ledUpdate(ap, x) // flashing white until the controller answers
	}
	if ap != nil {
		if _, err := os.Stat(filepath.Join(*stateDir, "system_cfg.txt")); err == nil {
			syncAP(ap, filepath.Join(*stateDir, "system_cfg.txt"), *apply, api, base)
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	client := &http.Client{Timeout: 10 * time.Second}
	interval := 10 * time.Second
	errs := 0
	lastListing := ""
	wlanBadChecks := 0
	var lastBan time.Time
	for {
		cfgBefore := x.S.CfgVersion
		next, ok := informOnce(x, client, statePath)
		if ok && !ledStarted {
			ledStarted = true
		}
		if ap != nil {
			for _, a := range next.Actions {
				runAction(ap, x, a)
			}
		}
		if ap != nil && x.S.CfgVersion != cfgBefore {
			syncAP(ap, filepath.Join(*stateDir, "system_cfg.txt"), *apply, api, base)
			lastBan = time.Time{} // a wifi reload drops hostapd's bans
			scans.rrmSoon()       // ... and its neighbour lists
		}
		if ap != nil {
			ledUpdate(ap, x)
			scans.background(ap)
		}
		if ap != nil && len(x.S.Blocked) > 0 && time.Since(lastBan) >= openwrt.BanTime/3 {
			if err := ap.EnforceBlocked(x.S.Blocked); err != nil {
				log.Printf("blocked clients: %v", err)
			} else {
				lastBan = time.Now()
			}
		}
		if next.Reset && *stopOnReset {
			log.Printf("STOP: device removed from the controller (setdefault); not re-listing as pending")
			return
		}
		switch {
		case ok:
			errs = 0
		default:
			errs++
			if errs >= *maxErrors {
				log.Printf("STOP: %d bad answers in a row", errs)
				return
			}
		}
		if next.Interval > 0 {
			interval = max(next.Interval, time.Second) // 1 s while the panel watches us
		}
		// Optional floor of our own (-inform-every; default off). The
		// controller itself asks for 1 s informs while the device is watched,
		// because we report inform_min_interval=1 (docs/internals.md).
		if *informEvery > 0 && interval > *informEvery {
			interval = max(*informEvery, time.Second)
		}

		if api != nil && time.Since(lastHealth) >= *healthEvery {
			lastHealth = time.Now()
			h, err := api.Health()
			if err != nil {
				log.Printf("STOP: health check failed: %v", err)
				return
			}
			// wlan dips while our own AP adopts/provisions; only a lasting
			// wlan problem, or any wan/lan/www problem, stops the daemon.
			var hard []string
			wlanBad := false
			for _, l := range h.Lost(base) {
				if l == "wlan" {
					wlanBad = true
				} else {
					hard = append(hard, l)
				}
			}
			if len(hard) > 0 {
				log.Printf("STOP: subsystem(s) no longer ok: %v (%s)", hard, fmtHealth(h))
				return
			}
			if wlanBad {
				wlanBadChecks++
				log.Printf("wlan not ok (%d/5 checks): %s", wlanBadChecks, fmtHealth(h))
				if wlanBadChecks >= 5 {
					log.Printf("STOP: wlan not ok for 5 checks")
					return
				}
			} else {
				wlanBadChecks = 0
			}
			if h.Uptime < base.Uptime {
				log.Printf("STOP: console restarted (uptime %d -> %d)", base.Uptime, h.Uptime)
				return
			}
			base.Uptime = h.Uptime
			if l := listing(api, mac); l != lastListing {
				log.Printf("UniFi: %s", l)
				lastListing = l
			}
		}

		wait := interval
		if next.Immediate {
			wait = 500 * time.Millisecond
		}
		select {
		case <-stop:
			log.Printf("stopped by signal")
			return
		case r := <-adoptReqs:
			o := x.ApplyAdopt(r)
			for _, e := range o.Events {
				log.Print(e)
			}
			if err := x.S.Save(statePath); err != nil {
				log.Printf("saving state: %v", err)
			}
			time.Sleep(time.Second) // let the console finish its SSH job first
		case <-time.After(wait):
		}
	}
}

// informOnce sends one inform and handles the answer. ok=false counts
// toward the error limit.
func informOnce(x *device.Session, c *http.Client, statePath string) (device.Outcome, bool) {
	js, err := x.Payload(time.Now())
	if err != nil {
		log.Printf("payload: %v", err)
		return device.Outcome{}, false
	}
	pkt, err := inform.Encode(x.Dev.MAC, x.S.Key, x.Cipher(), js, nil)
	if err != nil {
		log.Printf("encode: %v", err)
		return device.Outcome{}, false
	}
	req, _ := http.NewRequest(http.MethodPost, x.S.InformURL, bytes.NewReader(pkt))
	req.Header.Set("Content-Type", "application/x-binary")
	resp, err := c.Do(req)
	if err != nil {
		log.Printf("POST: %v", err)
		return device.Outcome{}, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		if !x.S.Adopted {
			return device.Outcome{}, true // pending: normal
		}
		log.Printf("HTTP 404 while adopted (forgotten?)")
		return device.Outcome{}, false
	default:
		log.Printf("HTTP %d (%d bytes)", resp.StatusCode, len(body))
		return device.Outcome{}, false
	}

	_, plain, err := inform.Decode(body, x.S.Key)
	if err != nil && x.S.Key != inform.DefaultKeyHex {
		_, plain, err = inform.Decode(body, inform.DefaultKeyHex)
	}
	if err != nil {
		log.Printf("reply did not decode: %v", err)
		return device.Outcome{}, false
	}
	o, err := x.Handle(plain, time.Now())
	if err != nil {
		log.Printf("%v", err)
		return o, false
	}
	for _, e := range o.Events {
		if !strings.HasPrefix(e, "noop ") || o.Immediate {
			log.Print(e)
		}
	}
	if o.Changed {
		if err := x.S.Save(statePath); err != nil {
			log.Printf("saving state: %v", err)
		}
	}
	return o, true
}

func listing(api *unifiapi.Client, mac net.HardwareAddr) string {
	devs, err := api.Devices()
	if err != nil {
		return "stat/device: " + err.Error()
	}
	for _, d := range devs {
		if strings.EqualFold(d.MAC, mac.String()) {
			return fmt.Sprintf("model=%s state=%d adopted=%v name=%q version=%s", d.Model, d.State, d.Adopted, d.Name, d.Version)
		}
	}
	return "not in stat/device"
}

func fmtHealth(h unifiapi.Health) string {
	var ok []string
	for k, v := range h.OK {
		if v {
			ok = append(ok, k)
		}
	}
	sort.Strings(ok)
	return fmt.Sprintf("network %s, console uptime %ds, %d devices, ok %v, GET %s",
		h.Version, h.Uptime, h.Devices, ok, h.Latency.Round(time.Millisecond))
}

func localIPToward(informURL string) (string, error) {
	host := strings.TrimPrefix(informURL, "http://")
	host, _, _ = strings.Cut(host, "/")
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	c, err := net.Dial("udp4", net.JoinHostPort(h, "9"))
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// syncAP plans the UCI changes the controller's system_cfg implies for the
// AP and, when apply is set, applies them with an automatic rollback that is
// cancelled only if the radios come back and the console stays healthy.
func syncAP(ap *openwrt.Client, cfgPath string, apply bool, api *unifiapi.Client, base unifiapi.Health) {
	syncUsers(ap, cfgPath, apply)
	defer syncGuest(ap, cfgPath, apply)
	defer syncSteering(ap, cfgPath, apply)
	defer syncLimits(ap, cfgPath, apply)
	defer syncMesh(ap, cfgPath, apply)
	if apply {
		defer syncSNMP(ap, cfgPath)
	}
	r, err := plan(ap, cfgPath)
	if err != nil {
		log.Printf("plan: %v", err)
		return
	}
	mode := "log-only, not applied"
	if apply {
		mode = "applying"
	}
	log.Printf("PLAN (%s): %d change(s)", mode, len(r.Ops))
	for _, op := range r.Ops {
		log.Printf("  %s", op)
	}
	for _, n := range r.Notes {
		log.Printf("  note: %s", n)
	}
	if apply {
		if err := ap.SyncSchedules(r.Schedules); err != nil {
			log.Printf("SCHEDULES FAILED: %v", err)
		} else if len(r.Schedules) > 0 {
			log.Printf("SCHEDULES: %d WLAN interface(s) on the AP's own schedule", len(r.Schedules))
		}
	}
	if !apply || len(r.Ops) == 0 {
		return
	}
	id, err := ap.Apply(r.Ops, 5*time.Minute)
	if err != nil {
		log.Printf("APPLY FAILED (rollback armed if it got that far): %v", err)
		if id != "" {
			ap.RollbackNow(id)
		}
		return
	}
	log.Printf("apply %s: committed, reloading; checking (rollback in 5 min unless confirmed)", id)
	time.Sleep(20 * time.Second)
	ok, why := ap.WifiHealthy()
	for try := 0; !ok && try < 3; try++ { // a network reload drops the SSH session; give it time
		time.Sleep(10 * time.Second)
		ok, why = ap.WifiHealthy()
	}
	if ok && api != nil {
		if h, err := api.Health(); err != nil {
			ok, why = false, "console health: "+err.Error()
		} else {
			for _, l := range h.Lost(base) {
				if l != "wlan" {
					ok, why = false, "console subsystem not ok: "+l
				}
			}
		}
	}
	if !ok {
		log.Printf("apply %s: NOT healthy (%s): rolling back now", id, why)
		if err := ap.RollbackNow(id); err != nil {
			log.Printf("rollback %s: %v (the timed rollback will still fire)", id, err)
		}
		return
	}
	if err := ap.Confirm(id); err != nil {
		log.Printf("apply %s: confirm failed: %v (it will roll back in 5 min)", id, err)
		return
	}
	log.Printf("apply %s: confirmed (%s)", id, why)
	if r2, err := plan(ap, cfgPath); err == nil {
		log.Printf("after apply: %d change(s) left", len(r2.Ops))
	}
}

func plan(ap *openwrt.Client, cfgPath string) (translate.Result, error) {
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		return translate.Result{}, err
	}
	out, err := ap.Run("uci show wireless; uci show system; uci show network")
	if err != nil {
		return translate.Result{}, fmt.Errorf("reading the AP's uci: %w", err)
	}
	f := ap.Features()
	f.APAddr = apHost
	return translate.Plan(translate.ParseCfg(string(cfg)), translate.ParseUCI(string(out)), f), nil
}

// runAction carries out a controller action on the real AP.
func runAction(ap *openwrt.Client, x *device.Session, a string) {
	if mac, ok := strings.CutPrefix(a, "guest-auth:"); ok {
		ttl := time.Until(time.Unix(x.S.Guests[mac], 0))
		log.Printf("GUEST AUTHORIZED: %s for %s", mac, ttl.Round(time.Minute))
		if err := ap.AuthorizeGuest(mac, ttl); err != nil {
			log.Printf("authorize guest: %v", err)
		}
		return
	}
	if mac, ok := strings.CutPrefix(a, "guest-unauth:"); ok {
		log.Printf("GUEST UNAUTHORIZED: %s", mac)
		if err := ap.UnauthorizeGuest(mac); err != nil {
			log.Printf("unauthorize guest: %v", err)
		}
		return
	}
	if mac, ok := strings.CutPrefix(a, "unblock:"); ok {
		log.Printf("UNBLOCK: %s", mac)
		if err := ap.Unblock([]string{mac}); err != nil {
			log.Printf("unblock: %v", err)
		}
		return
	}
	if mac, ok := strings.CutPrefix(a, "kick:"); ok {
		log.Printf("KICK: disconnecting %s (Reconnect from the controller)", mac)
		if err := ap.Kick(mac); err != nil {
			log.Printf("kick: %v", err)
		}
		return
	}
	switch a {
	case "scan", "spectrum-scan":
		log.Printf("SCAN: %s requested by the controller (radios leave their channel for a few seconds)", a)
		scans.start(ap, true, a == "spectrum-scan")
	case "led:on", "led:off", "locate:off":
		ledUpdate(ap, x)
	case "locate:on":
		ledUpdate(ap, x)
		if !locateBeepOn {
			break
		}
		if ok, err := ap.Beep(); err != nil {
			log.Printf("locate beep: %v", err)
		} else if ok {
			log.Printf("locate: beeping")
		}
	case "block":
		log.Printf("BLOCK: %d client(s) blocked: %v", len(x.S.Blocked), x.S.Blocked)
		if err := ap.EnforceBlocked(x.S.Blocked); err != nil {
			log.Printf("block: %v", err)
		}
	case "reboot":
		log.Printf("RESTART: rebooting the AP (requested by the controller)")
		if err := ap.Reboot(); err != nil {
			log.Printf("reboot: %v", err)
			return
		}
		waitForAP(ap, x)
	}
}

// waitForAP pauses informing while the AP reboots (a real AP goes quiet
// too), then re-applies the LED state.
func waitForAP(ap *openwrt.Client, x *device.Session) {
	time.Sleep(20 * time.Second)
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		if s, err := ap.Collect(); err == nil {
			log.Printf("AP back after reboot (uptime %ds)", s.Uptime)
			x.Boot = time.Now().Add(-time.Duration(s.Uptime) * time.Second)
			ledUpdate(ap, x)
			if guestCfgPath != "" {
				syncGuest(ap, guestCfgPath, true)
			}
			return
		}
		time.Sleep(5 * time.Second)
	}
	log.Printf("AP not back 5 min after reboot; resuming informs anyway")
}

// apHost: the AP address (from -ap), which a static IP must keep.
var apHost string

// locateBeepOn: -locate-beep.
var locateBeepOn bool

// syncUsers gives the AP the controller's device logins (Device
// Authentication) as extra admin accounts.
func syncUsers(ap *openwrt.Client, cfgPath string, apply bool) {
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	users, notes := translate.Users(translate.ParseCfg(string(cfg)))
	for _, n := range notes {
		log.Printf("  users note: %s", n)
	}
	if !apply {
		names := []string{}
		for _, u := range users {
			names = append(names, u.Name)
		}
		log.Printf("USERS (log-only): would keep device logins %v", names)
		return
	}
	changed, err := ap.SyncUsers(users)
	if err != nil {
		log.Printf("USERS FAILED: %v", err)
		return
	}
	for _, c := range changed {
		log.Printf("USERS: %s", c)
	}
}

// guestSession: the session (for re-adding authorized guests).
var guestSession *device.Session

// guestCfgPath: system_cfg path, for re-applying guest rules after a reboot.
var guestCfgPath string

// syncGuest installs the controller's guest-network restrictions on the AP's
// guest interfaces (nftables bridge table unifi_guest), or removes them.
func syncGuest(ap *openwrt.Client, cfgPath string, apply bool) {
	guestCfgPath = cfgPath
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	g := translate.GuestPolicy(translate.ParseCfg(string(b)))
	snap, err := ap.Collect()
	if err != nil {
		log.Printf("guest: %v", err)
		return
	}
	byDev := snap.IfnamesByDevname(openwrt.InfoFromCfg(string(b)))
	var ifs []string
	for _, d := range g.Devnames {
		if i := byDev[d]; i != "" {
			ifs = append(ifs, i)
		}
	}
	if !apply {
		log.Printf("GUEST (log-only): guest interfaces %v", ifs)
		return
	}
	if err := ap.SetGuest(ifs, g); err != nil {
		log.Printf("GUEST FAILED: %v", err)
		return
	}
	// the table was rebuilt: put the portal-authorized guests back
	if guestSession != nil {
		for m, end := range guestSession.S.Guests {
			if ttl := time.Until(time.Unix(end, 0)); ttl > 0 {
				ap.AuthorizeGuest(m, ttl)
			} else {
				delete(guestSession.S.Guests, m)
			}
		}
	}
	if len(ifs) > 0 {
		log.Printf("GUEST: restrictions on %v (blocked %v; guest DNS -> forwarder on the AP)", ifs, g.Restricted)
	}
}

// syncSteering maps the controller's band steering onto usteer.
func syncSteering(ap *openwrt.Client, cfgPath string, apply bool) {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	cfg := translate.ParseCfg(string(b))
	st := translate.BandSteering(cfg)
	ssidOf := map[string]string{}
	for k, d := range openwrt.InfoFromCfg(string(b)).DevName {
		ssid, _, _ := strings.Cut(k, "|")
		ssidOf[d] = ssid
	}
	seen := map[string]bool{}
	var ssids []string
	for _, d := range st.Devnames {
		if s := ssidOf[d]; s != "" && !seen[s] {
			seen[s] = true
			ssids = append(ssids, s)
		}
	}
	if !apply {
		log.Printf("BANDSTEERING (log-only): mode %q on %v", st.Mode, ssids)
		return
	}
	if err := ap.SetBandSteering(st.Mode, ssids); err != nil {
		log.Printf("BANDSTEERING FAILED: %v", err)
		return
	}
	if st.Mode != "" {
		log.Printf("BANDSTEERING: %s on %v (usteer, local mode)", st.Mode, ssids)
	}
}

// syncLimits applies the controller's per-client rate limits (user groups).
func syncLimits(ap *openwrt.Client, cfgPath string, apply bool) {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	lims := translate.QoSLimits(translate.ParseCfg(string(b)))
	var ifl []openwrt.IfLimit
	if len(lims) > 0 {
		snap, err := ap.Collect()
		if err != nil {
			log.Printf("limits: %v", err)
			return
		}
		byDev := snap.IfnamesByDevname(openwrt.InfoFromCfg(string(b)))
		for _, l := range lims {
			if i := byDev[l.Devname]; i != "" {
				ifl = append(ifl, openwrt.IfLimit{Ifname: i, DownKbps: l.DownKbps, UpKbps: l.UpKbps})
			}
		}
	}
	if !apply {
		log.Printf("LIMITS (log-only): %+v", ifl)
		return
	}
	// per-client shaping with tc when the AP has it (as a UniFi AP);
	// otherwise per-client policing with nftables meters
	shapeMu.Lock()
	shapeUseTC = len(ifl) > 0 && ap.HasTC()
	shapeLimits = nil
	if shapeUseTC {
		shapeLimits = ifl
	}
	shapeLast = "" // reconcile on the next AP read
	shapeMu.Unlock()
	if shapeUseTC || len(ifl) == 0 {
		ap.SetLimits(nil)
		if len(ifl) == 0 {
			shapeReconcile(ap, nil)
		}
		if shapeUseTC {
			log.Printf("LIMITS: per-client shaping (tc) on %+v", ifl)
		}
		return
	}
	if err := ap.SetLimits(ifl); err != nil {
		log.Printf("LIMITS FAILED: %v", err)
		return
	}
	if len(ifl) > 0 {
		log.Printf("LIMITS: per-client rate limits %+v", ifl)
	}
}

var (
	shapeMu     sync.Mutex
	shapeUseTC  bool
	shapeLimits []openwrt.IfLimit
	shapeLast   string
	shapedIfs   []string
	shapeAt     time.Time
)

// shapeReconcile makes the AP's per-client shaping match the stations now
// on the rate-limited interfaces (s nil: remove all shaping).
func shapeReconcile(ap *openwrt.Client, s *openwrt.Snapshot) {
	shapeMu.Lock()
	defer shapeMu.Unlock()
	var want []openwrt.IfShape
	if s != nil {
		stations := map[string][]string{}
		for _, r := range s.Radios {
			for _, v := range r.VAPs {
				for _, st := range v.Stations {
					stations[v.Iface] = append(stations[v.Iface], strings.ToLower(st.MAC))
				}
			}
		}
		for _, l := range shapeLimits {
			want = append(want, openwrt.IfShape{Ifname: l.Ifname, DownKbps: l.DownKbps, UpKbps: l.UpKbps, Stations: stations[l.Ifname]})
		}
	}
	key := fmt.Sprintf("%+v", want)
	if time.Since(shapeAt) > 5*time.Minute {
		shapeLast = "" // re-assert: a Wi-Fi reload re-creates the interfaces
	}
	if key == shapeLast {
		return
	}
	shapeAt = time.Now()
	now := map[string]bool{}
	for _, w := range want {
		now[w.Ifname] = true
	}
	var clear []string
	for _, i := range shapedIfs {
		if !now[i] {
			clear = append(clear, i)
		}
	}
	if len(want) == 0 && len(clear) == 0 {
		shapeLast = key
		return
	}
	if err := ap.SetShaping(want, clear); err != nil {
		log.Printf("SHAPING FAILED: %v", err)
		return
	}
	shapeLast = key
	shapedIfs = nil
	for _, w := range want {
		shapedIfs = append(shapedIfs, w.Ifname)
		if len(w.Stations) > 0 {
			log.Printf("SHAPING: %s %d/%d kbps per client, %d client(s)", w.Ifname, w.DownKbps, w.UpKbps, len(w.Stations))
		}
	}
	if len(clear) > 0 {
		log.Printf("SHAPING: removed from %v", clear)
	}
}

// meshUplink: -mesh-uplink.
var meshUplink bool

// syncMesh gives the AP-side watcher the site's mesh SSID/PSK (system_cfg
// mesh.essid + the vwire VAP's aaa.N.wpa.psk) and the 5 GHz radio.
func syncMesh(ap *openwrt.Client, cfgPath string, apply bool) {
	if !apply {
		return
	}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	cfg := translate.ParseCfg(string(b))
	ssid, psk := cfg["mesh.essid"], ""
	for k, v := range cfg {
		if strings.HasPrefix(k, "aaa.") && strings.HasSuffix(k, ".ssid") && v == ssid && ssid != "" {
			psk = cfg[strings.TrimSuffix(k, "ssid")+"wpa.psk"]
		}
	}
	radio := ""
	if out, err := ap.Run("uci show wireless"); err == nil {
		u := translate.ParseUCI(string(out))
		for k, v := range u {
			if sec, ok := strings.CutSuffix(strings.TrimPrefix(k, "wireless."), ".band"); ok && v == "5g" && !strings.Contains(sec, ".") {
				if radio == "" || sec < radio {
					radio = sec
				}
			}
		}
	}
	on := meshUplink && cfg["mesh.status"] == "enabled" && ssid != "" && psk != "" && radio != ""
	// Mesh Parent in the UniFi (device mesh_uplink_1/2 -> mesh.serial1/2)
	accept := openwrt.MeshAccept(cfg["mesh.serial1"], cfg["mesh.serial2"])
	if err := ap.SyncMesh(on, ssid, psk, radio, accept); err != nil {
		log.Printf("MESH FAILED: %v", err)
		return
	}
	if on {
		parent := "any parent"
		if len(accept) > 0 {
			parent = "parent " + strings.TrimSpace(cfg["mesh.serial1"]+" "+cfg["mesh.serial2"])
		}
		log.Printf("MESH: wireless uplink armed on %s, %s (joins only when no cable has link)", radio, parent)
	}
}

// portalListen: -portal-listen.
var portalListen string

// portalServer takes the controller's post-login redirect
// (http://<AP>:8882/redirect?ec=...): it opens the cookie with this AP's
// guest_token, authorizes the guest's MAC until the cookie's expiry, and sends
// the browser on to where it was going.
func portalServer(addr string, ap *openwrt.Client, x *device.Session, apMAC, statePath string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		m, err := device.DecryptEC(x.S.GuestToken, r.URL.Query().Get("ec"))
		var g device.PortalGuest
		if err == nil {
			g, err = device.ParsePortalGuest(m, apMAC, time.Now().Unix())
		}
		if err != nil {
			log.Printf("PORTAL: redirect from %s refused: %v", r.RemoteAddr, err)
			http.Error(w, "invalid or expired authorization", http.StatusForbidden)
			return
		}
		ttl := time.Until(time.Unix(g.Expire, 0))
		// copy-on-write: the inform loop reads S.Guests without the lock
		x.GuestMu.Lock()
		guests := make(map[string]int64, len(x.S.Guests)+1)
		for k, v := range x.S.Guests {
			guests[k] = v
		}
		guests[g.MAC] = g.Expire
		x.S.Guests = guests
		if err := x.S.Save(statePath); err != nil {
			log.Printf("PORTAL: saving state: %v", err)
		}
		x.GuestMu.Unlock()
		log.Printf("PORTAL: guest %s authorized for %s (portal login)", g.MAC, ttl.Round(time.Minute))
		if err := ap.AuthorizeGuest(g.MAC, ttl); err != nil {
			log.Printf("PORTAL: authorize %s: %v", g.MAC, err)
		}
		if strings.HasPrefix(g.URL, "http://") || strings.HasPrefix(g.URL, "https://") {
			http.Redirect(w, r, g.URL, http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>Connected</title><p>You are connected.</p>")
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("PORTAL: redirect endpoint on %s", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("PORTAL: %v", err)
	}
}

// syslogForwarder sends this AP's log to the controller the UniFi way when
// system_cfg asks for encrypted remote syslog (syslog.remote.encrypt): each
// logread line, re-tagged "MODEL,mac,version:", sealed per datagram
// (device.SealSyslog). Plain remote syslog stays with logd (planSyslog).
func syslogForwarder(x *device.Session, stateDir string) {
	type target struct{ addr, key string }
	var cur target
	var stop func()
	for {
		b, _ := os.ReadFile(filepath.Join(stateDir, "system_cfg.txt"))
		c := translate.ParseCfg(string(b))
		want := target{}
		if c["syslog.status"] == "enabled" && c["syslog.remote.status"] == "enabled" &&
			c["syslog.remote.encrypt"] == "enabled" && c["syslog.remote.ip"] != "" && len(c["syslog.remote.key"]) == 64 {
			port := c["syslog.remote.port"]
			if port == "" {
				port = "5514"
			}
			want = target{net.JoinHostPort(c["syslog.remote.ip"], port), c["syslog.remote.key"]}
		}
		if want != cur {
			if stop != nil {
				stop()
				stop = nil
				log.Printf("SYSLOG: encrypted forwarding to %s stopped", cur.addr)
			}
			if want.addr != "" {
				s, err := startSyslog(x, want.addr, want.key)
				if err != nil {
					log.Printf("SYSLOG: %v", err)
					want = target{} // retry next round
				} else {
					stop = s
					log.Printf("SYSLOG: encrypted forwarding to %s (hash_id in informs)", want.addr)
				}
			}
			cur = want
		}
		time.Sleep(30 * time.Second)
	}
}

func startSyslog(x *device.Session, addr, key string) (func(), error) {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("logread", "-f")
	out, err := cmd.StdoutPipe()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		conn.Close()
		return nil, err
	}
	tag := fmt.Sprintf("%s,%s,%s:", x.Dev.Model, strings.ReplaceAll(strings.ToLower(x.Dev.MAC.String()), ":", ""), x.S.Version)
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 8192), 8192)
		tokens, last := 100.0, time.Now()
		failed := false
		for sc.Scan() {
			now := time.Now()
			tokens = min(100, tokens+now.Sub(last).Seconds()*50) // 50/s, bursts of 100
			last = now
			if tokens < 1 {
				continue
			}
			tokens--
			d, err := device.SealSyslog(key, x.S.HashID, []byte(uafSyslogLine(sc.Text(), x.Dev.Hostname, tag)))
			if err == nil {
				_, err = conn.Write(d)
			}
			if err != nil && !failed {
				failed = true
				log.Printf("SYSLOG: send: %v (further errors not logged)", err)
			}
		}
		cmd.Wait()
	}()
	return func() { cmd.Process.Kill(); conn.Close() }, nil
}

// uafSyslogLine turns a logread line ("Fri Nov 13 00:35:06 2026 daemon.err
// uow[550]: msg") into a UniFi AP style syslog line
// ("<27>Nov 13 00:35:06 OnHub U7PG2,085566770001,6.8.2.15592: uow[550]: msg").
func uafSyslogLine(line, host, tag string) string {
	f := strings.SplitN(line, " ", 7)
	if len(f) < 7 {
		return fmt.Sprintf("<14>%s %s %s %s", time.Now().Format(time.Stamp), host, tag, line)
	}
	pri := 14
	if fac, lvl, ok := strings.Cut(f[5], "."); ok {
		pri = syslogFacility[fac]*8 + syslogLevel[lvl]
	}
	return fmt.Sprintf("<%d>%s %2s %s %s %s %s", pri, f[1], f[2], f[3], host, tag, f[6])
}

var syslogFacility = map[string]int{"kern": 0, "user": 1, "mail": 2, "daemon": 3, "auth": 4, "syslog": 5,
	"lpr": 6, "news": 7, "uucp": 8, "cron": 9, "authpriv": 10, "ftp": 11, "local0": 16, "local1": 17,
	"local2": 18, "local3": 19, "local4": 20, "local5": 21, "local6": 22, "local7": 23}
var syslogLevel = map[string]int{"emerg": 0, "alert": 1, "crit": 2, "err": 3, "warn": 4, "warning": 4,
	"notice": 5, "info": 6, "debug": 7}

// Status LED (UniFi AP behaviour, docs/internals.md).
var (
	ledStarted bool              // the controller has answered once
	ledShown   *openwrt.LEDState // what the LEDs show now
	ledCfgPath string            // system_cfg.txt (ledbar.* colour/brightness)
)

// ledWanted: flashing white while starting, white until adopted, fast blue
// flashing for Locate (also with the LED off), dark when the LED is off,
// else blue or the controller's ledbar colour at its brightness.
func ledWanted(x *device.Session) openwrt.LEDState {
	switch {
	case !ledStarted:
		s := openwrt.LEDWhite
		s.Blink = openwrt.Flash
		return s
	case !x.S.Adopted:
		return openwrt.LEDWhite
	case x.S.Locating:
		s := openwrt.LEDBlue
		s.Blink = openwrt.FastFlash
		return s
	}
	b, _ := os.ReadFile(ledCfgPath)
	c := translate.ParseCfg(string(b))
	if !x.S.LED || c["ledbar.status"] == "disabled" {
		return openwrt.LEDDark
	}
	s := openwrt.LEDBlue
	byte8 := func(k string) (uint8, bool) {
		v, err := strconv.Atoi(c[k])
		return uint8(min(max(v, 0), 255)), err == nil
	}
	if r, ok1 := byte8("ledbar.color.1.r"); ok1 {
		g, _ := byte8("ledbar.color.1.g")
		bl, _ := byte8("ledbar.color.1.b")
		if r|g|bl != 0 {
			s.R, s.G, s.B = r, g, bl
		}
	}
	if br, ok := byte8("ledbar.brightness"); ok && br > 0 {
		s.Brightness = br
	}
	return s
}

// ledIdle: -led-idle; ledRainbow: the running idle effect.
var (
	ledIdle    string
	ledRainbow *openwrt.Rainbow
)

func ledUpdate(ap *openwrt.Client, x *device.Session) {
	w := ledWanted(x)
	if ledShown != nil && *ledShown == w {
		return
	}
	ledRainbow.Stop()
	ledRainbow = nil
	if err := ap.ShowLED(w); err != nil {
		log.Printf("LED %s: %v", w, err)
		return
	}
	ledShown = &w
	// adopted and running (steady, lit, not Locate): the optional idle effect
	idle := ledStarted && x.S.Adopted && !x.S.Locating && w.Blink == openwrt.Steady && w != openwrt.LEDDark
	if ledIdle == "rainbow" && idle && ap.IsLocal() {
		if leds, err := ap.LEDs(); err == nil {
			// one turn a minute, a 10 s fade from blue, at 50 % of the state's brightness
			ledRainbow = openwrt.StartRainbow(leds, uint8(int(w.Brightness)/2), time.Minute, 10*time.Second)
		}
	}
	if ledRainbow != nil {
		log.Printf("LED -> %s, fading into rainbow", w)
		return
	}
	log.Printf("LED -> %s", w)
}

// hwCaps: -hw-caps.
var hwCaps int

// syncSNMP: the controller's snmp.* -> snmpd (internal/openwrt/snmp.go).
func syncSNMP(ap *openwrt.Client, cfgPath string) {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return
	}
	conf, err := openwrt.SNMPConfig(translate.ParseCfg(string(b)))
	if err != nil {
		log.Printf("SNMP: %v", err)
		return
	}
	changed, err := ap.SyncSNMP(conf)
	switch {
	case err != nil:
		log.Printf("SNMP: %v", err)
	case changed && conf != "":
		log.Printf("SNMP: agent configured (snmpd)")
	case changed:
		log.Printf("SNMP: off")
	}
}

// minRSSIKick: UniFi minimum RSSI also drops connected clients that stay
// below it (hostapd's rssi_reject_assoc_rssi only refuses new ones): below
// the radio's minimum on every reading for 30 s -> disconnect.
var lowRSSI = map[string]time.Time{} // first reading below the minimum

const lowRSSIFor = 30 * time.Second

func minRSSIKick(ap *openwrt.Client, s *openwrt.Snapshot) {
	if s == nil {
		return
	}
	b, _ := os.ReadFile(ledCfgPath) // system_cfg.txt
	c := translate.ParseCfg(string(b))
	min := map[string]int{}
	for i := 1; i <= 8; i++ {
		p := fmt.Sprintf("stamgr.%d.", i)
		if c[p+"minrssi.status"] != "true" {
			continue
		}
		v, err := strconv.Atoi(c[p+"minrssi.rssi"])
		if err != nil || v == 0 {
			continue
		}
		if v > 0 {
			v -= 95
		}
		min[c[p+"radio"]] = v
	}
	seen := map[string]bool{}
	for _, r := range s.Radios {
		m, ok := min[r.Band]
		for _, v := range r.VAPs {
			if v.Mode == "Client" {
				continue // our own mesh uplink
			}
			for _, st := range v.Stations {
				if !ok || st.Signal == 0 || st.Signal >= m {
					continue
				}
				seen[st.MAC] = true
				if lowRSSI[st.MAC].IsZero() {
					lowRSSI[st.MAC] = time.Now()
				}
				if time.Since(lowRSSI[st.MAC]) >= lowRSSIFor {
					log.Printf("MIN RSSI: %s at %d dBm < %d on %s: disconnecting", st.MAC, st.Signal, m, r.Band)
					if err := ap.Kick(st.MAC); err != nil {
						log.Printf("min rssi kick: %v", err)
					}
					delete(lowRSSI, st.MAC)
				}
			}
		}
	}
	for m := range lowRSSI {
		if !seen[m] {
			delete(lowRSSI, m)
		}
	}
}
