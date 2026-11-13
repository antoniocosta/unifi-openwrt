// SPDX-License-Identifier: GPL-2.0-or-later

package inform

// Interop with other implementations, by files (no import of their code):
//   INFORM_INTEROP_OUT=dir  writes our packets (pending-cbc.bin, adopted-gcm.bin)
//   INFORM_INTEROP_IN=dir   decodes *.reply.bin written by the other side
// Skipped unless one of them is set. See docs/internals.md.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const interopAdoptedKey = "0123456789abcdef0123456789abcdef"

func TestInteropWrite(t *testing.T) {
	dir := os.Getenv("INFORM_INTEROP_OUT")
	if dir == "" {
		t.Skip("INFORM_INTEROP_OUT not set")
	}
	body := []byte(`{"_type":"info","mac":"02:00:5e:10:00:01","model":"U7PG2","state":1}`)
	for name, c := range map[string]struct {
		c   Cipher
		key string
	}{"pending-cbc": {CBC, DefaultKeyHex}, "adopted-gcm": {GCM, interopAdoptedKey}} {
		pkt, err := Encode(testMAC, c.key, c.c, body, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".bin"), pkt, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInteropRead(t *testing.T) {
	dir := os.Getenv("INFORM_INTEROP_IN")
	if dir == "" {
		t.Skip("INFORM_INTEROP_IN not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.reply.bin"))
	if len(files) == 0 {
		t.Fatal("no *.reply.bin files")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var lastErr error
		ok := false
		for _, k := range []string{DefaultKeyHex, interopAdoptedKey} {
			h, js, err := Decode(b, k)
			if err != nil {
				lastErr = err
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(js, &m); err != nil {
				lastErr = err
				continue
			}
			t.Logf("%s: flags=%#04x json=%s", filepath.Base(f), h.Flags, js)
			ok = true
			break
		}
		if !ok {
			t.Errorf("%s: %v", filepath.Base(f), lastErr)
		}
	}
}
