// SPDX-License-Identifier: GPL-2.0-or-later

package device

// SSH adoption endpoint. UniFi Network 10.6 adopts a pending device by
// SSHing to it (user ubnt) and running
//   /usr/bin/syswrapper.sh set-adopt <inform url> <authkey>
// (open-unifi docs/PROTOCOL-mgmt.md §7; seen live on Network 10.6: "Invalid user
// ubnt from <console>" in the daemon host's own sshd log). The device reports the port
// in its inform payload (`sshd_port`).
//
// This is not a shell: it only parses set-adopt (and set-inform), answers
// every other command with empty output and exit 0, and runs nothing.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// AdoptRequest is what set-adopt delivered.
type AdoptRequest struct {
	URL, Key string
}

type SSHAdopt struct {
	Listen    string   // e.g. 192.168.1.50:2222
	AllowFrom []string // source IPs allowed to connect (the console)
	HostKey   string   // path to the PEM host key (created mode 600)
	Requests  chan AdoptRequest
	mu        sync.Mutex
	ln        net.Listener
}

var (
	setAdoptRE = regexp.MustCompile(`set-adopt\s+(\S+)\s+([0-9a-fA-F]{32})`)
	setInfRE   = regexp.MustCompile(`set-inform\s+(\S+)`)
)

func (s *SSHAdopt) Start() error {
	signer, err := loadOrCreateHostKey(s.HostKey)
	if err != nil {
		return err
	}
	// The console only accepts RSA host keys (rsa-sha2-512/256, ssh-rsa;
	// seen live on Network 10.6), like the dropbear on a real UAP.
	rsaSigner, err := loadOrCreateRSAHostKey(strings.TrimSuffix(s.HostKey, ".pem") + "_rsa.pem")
	if err != nil {
		return err
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
			// Any password: the console uses the factory login (ubnt) or the
			// site's device password. The source IP is already restricted.
			log.Printf("ssh-adopt: login user=%q from %s", c.User(), c.RemoteAddr())
			return nil, nil
		},
		ServerVersion: "SSH-2.0-dropbear_2019.78",
	}
	cfg.AddHostKey(rsaSigner)
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", s.Listen)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					log.Printf("ssh-adopt: accept: %v", err)
				}
				return
			}
			host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
			if !contains(s.AllowFrom, host) {
				log.Printf("ssh-adopt: refused connection from %s (not the console)", host)
				c.Close()
				continue
			}
			go s.serve(c, cfg)
		}
	}()
	return nil
}

func (s *SSHAdopt) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		s.ln.Close()
	}
}

func (s *SSHAdopt) serve(c net.Conn, cfg *ssh.ServerConfig) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Minute))
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		log.Printf("ssh-adopt: handshake: %v", err)
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, in, err := nc.Accept()
		if err != nil {
			continue
		}
		go s.session(ch, in)
	}
}

func (s *SSHAdopt) session(ch ssh.Channel, in <-chan *ssh.Request) {
	defer ch.Close()
	for req := range in {
		switch req.Type {
		case "exec":
			var p struct{ Cmd string }
			ssh.Unmarshal(req.Payload, &p)
			req.Reply(true, nil)
			s.command(p.Cmd)
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		case "shell":
			req.Reply(true, nil)
			// Read commands line by line until EOF.
			buf, _ := io.ReadAll(io.LimitReader(ch, 64<<10))
			for _, line := range strings.Split(string(buf), "\n") {
				if strings.TrimSpace(line) != "" {
					s.command(line)
				}
			}
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		default:
			req.Reply(req.Type == "pty-req" || req.Type == "env", nil)
		}
	}
}

func (s *SSHAdopt) command(cmd string) {
	if m := setAdoptRE.FindStringSubmatch(cmd); m != nil {
		log.Printf("ssh-adopt: set-adopt url=%s key=<masked>", m[1])
		s.Requests <- AdoptRequest{URL: m[1], Key: strings.ToLower(m[2])}
		return
	}
	if m := setInfRE.FindStringSubmatch(cmd); m != nil {
		log.Printf("ssh-adopt: set-inform url=%s", m[1])
		s.Requests <- AdoptRequest{URL: m[1]}
		return
	}
	log.Printf("ssh-adopt: command ignored: %q", maskHex(cmd))
}

var hex32 = regexp.MustCompile(`[0-9a-fA-F]{32}`)

func maskHex(s string) string { return hex32.ReplaceAllString(s, "<masked>") }

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func loadOrCreateHostKey(path string) (ssh.Signer, error) {
	if b, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(b)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "unifi-openwrt fake device")
	if err != nil {
		return nil, err
	}
	b := pem.EncodeToMemory(blk)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	return ssh.ParsePrivateKey(b)
}

// ApplyAdopt stores a set-adopt/set-inform request in the session.
func (x *Session) ApplyAdopt(r AdoptRequest) Outcome {
	o := Outcome{Changed: true, Immediate: true}
	if r.URL != "" {
		x.S.InformURL = r.URL
	}
	if r.Key != "" {
		x.S.Key = r.Key
		x.S.Adopted = true
		o.Events = append(o.Events, "ADOPTED via SSH set-adopt: new authkey stored, inform_url="+x.S.InformURL)
	} else {
		o.Events = append(o.Events, "set-inform: inform_url="+x.S.InformURL)
	}
	return o
}

func loadOrCreateRSAHostKey(path string) (ssh.Signer, error) {
	if b, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(b)
	}
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, fmt.Errorf("rsa host key: %w", err)
	}
	return ssh.ParsePrivateKey(b)
}
