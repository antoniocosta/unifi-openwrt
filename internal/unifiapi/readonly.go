// SPDX-License-Identifier: GPL-2.0-or-later

// Package unifiapi is a GET-only client for the UniFi Network API on the
// console, used for optional health checks. It has no way to send anything
// but GET: every change to the UniFi is the user's own click.
package unifiapi

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Client struct {
	base string // e.g. https://192.168.1.1/proxy/network/api/s/default
	key  string
	http *http.Client
}

// KeyFromSecrets returns the UniFi API key: the environment variable
// UNIFI_API_KEY, or else the UNIFI_API_KEY= line (optionally prefixed with
// "export ") of the file named by UNIFI_API_KEY_FILE. The file is read, never
// sourced.
func KeyFromSecrets() (string, error) {
	if v := os.Getenv("UNIFI_API_KEY"); v != "" {
		return v, nil
	}
	path := os.Getenv("UNIFI_API_KEY_FILE")
	if path == "" {
		return "", fmt.Errorf("UNIFI_API_KEY not set (set it, or UNIFI_API_KEY_FILE to a file holding UNIFI_API_KEY=...)")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")
		if v, ok := strings.CutPrefix(line, "UNIFI_API_KEY="); ok {
			return strings.Trim(v, `"'`), nil
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("UNIFI_API_KEY not found in %s", path)
}

func New(consoleHost, site, key string) *Client {
	return &Client{
		base: "https://" + consoleHost + "/proxy/network/api/s/" + site,
		key:  key,
		http: &http.Client{
			Timeout: 10 * time.Second,
			// The console uses a self-signed certificate on the LAN.
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		},
	}
}

// get is the only request this package can make.
func (c *Client) get(path string, out any) (time.Duration, error) {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-API-KEY", c.key)
	start := time.Now()
	resp, err := c.http.Do(req)
	took := time.Since(start)
	if err != nil {
		return took, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return took, err
	}
	if resp.StatusCode != http.StatusOK {
		return took, fmt.Errorf("GET %s: HTTP %d", path, resp.StatusCode)
	}
	return took, json.Unmarshal(body, out)
}

// Health is a quick read of the console's state, used before and after
// every packet M1 sends.
type Health struct {
	Latency time.Duration
	Version string          // Network application version
	Uptime  int64           // console uptime, seconds (a drop means it rebooted)
	Devices int             // devices in stat/device
	OK      map[string]bool // stat/health: subsystem (wan, lan, wlan, www, ...) -> status == "ok"
}

// Lost lists subsystems that were ok in h0 and are not ok in h.
func (h Health) Lost(h0 Health) []string {
	var lost []string
	for k, ok := range h0.OK {
		if ok && !h.OK[k] {
			lost = append(lost, k)
		}
	}
	return lost
}

func (c *Client) Health() (Health, error) {
	var h Health
	var sys struct {
		Data []struct {
			Version string `json:"version"`
			Uptime  int64  `json:"uptime"`
		} `json:"data"`
	}
	took, err := c.get("/stat/sysinfo", &sys)
	if err != nil {
		return h, err
	}
	h.Latency = took
	if len(sys.Data) > 0 {
		h.Version, h.Uptime = sys.Data[0].Version, sys.Data[0].Uptime
	}
	devs, err := c.Devices()
	if err != nil {
		return h, err
	}
	h.Devices = len(devs)
	var hs struct {
		Data []struct {
			Subsystem string `json:"subsystem"`
			Status    string `json:"status"`
		} `json:"data"`
	}
	if _, err := c.get("/stat/health", &hs); err != nil {
		return h, err
	}
	h.OK = map[string]bool{}
	for _, x := range hs.Data {
		h.OK[x.Subsystem] = x.Status == "ok"
	}
	return h, nil
}

type Device struct {
	MAC     string `json:"mac"`
	Model   string `json:"model"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	State   int    `json:"state"`
	Adopted bool   `json:"adopted"`
	IP      string `json:"ip"`
	Version string `json:"version"`
}

func (c *Client) Devices() ([]Device, error) {
	var r struct {
		Data []Device `json:"data"`
	}
	_, err := c.get("/stat/device", &r)
	return r.Data, err
}
