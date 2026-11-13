// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// beepWAV builds a short double-beep (48 kHz, 16-bit stereo PCM WAV): the
// "Locate" sound for APs with a speaker.
func beepWAV() []byte {
	const rate = 48000
	var pcm []int16
	tone := func(sec, freq float64) {
		n := int(rate * sec)
		for i := 0; i < n; i++ {
			env := math.Min(1, math.Min(float64(i)/240, float64(n-i)/240))
			v := int16(32767 * 0.15 * env * math.Sin(2*math.Pi*freq*float64(i)/rate))
			pcm = append(pcm, v, v)
		}
	}
	gap := func(sec float64) { pcm = append(pcm, make([]int16, 2*int(rate*sec))...) }
	for k := 0; k < 3; k++ {
		tone(0.12, 2400)
		gap(0.08)
		tone(0.12, 2400)
		gap(0.4)
	}
	var b bytes.Buffer
	data := len(pcm) * 2
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(2), uint32(rate), uint32(rate * 4), uint16(4), uint16(16)} {
		binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data))
	binary.Write(&b, binary.LittleEndian, pcm)
	return b.Bytes()
}

// Beep plays the locate sound on the AP's speaker, if it has a sound card
// and aplay. It returns false (no error) when the AP cannot beep.
func (cl *Client) Beep() (bool, error) {
	out, err := cl.RunStdin(`command -v aplay >/dev/null && grep -q . /proc/asound/cards 2>/dev/null || { echo nobeep; exit 0; }
cat > /tmp/unifi-locate.wav && (aplay -q /tmp/unifi-locate.wav >/dev/null 2>&1 &) ; echo beep`, beepWAV())
	if err != nil {
		return false, fmt.Errorf("beep: %v", err)
	}
	return strings.Contains(string(out), "beep") && !strings.Contains(string(out), "nobeep"), nil
}
