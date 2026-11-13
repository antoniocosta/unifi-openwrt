// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// UniFi AP status LED (docs/internals.md): steady white = factory default /
// not adopted, flashing white = starting, alternating white/blue = busy,
// steady blue = adopted and running, fast blue flashing = Locate, dark = LED
// off. Models with an LED bar (hw_caps 2/16384) also take a colour and a
// brightness from system_cfg (ledbar.*).

// Blink patterns.
const (
	Steady    = ""
	Flash     = "flash"     // ~1 Hz (starting)
	FastFlash = "fastflash" // ~3 Hz (Locate)
	Alternate = "alternate" // white <-> blue (busy)
)

// LEDState is what the status LEDs should show.
type LEDState struct {
	R, G, B    uint8 // colour; all 0 = dark
	Brightness uint8 // 0-255 (255 = full)
	Blink      string
}

// Standard UniFi states.
var (
	LEDWhite = LEDState{R: 255, G: 255, B: 255, Brightness: 255}
	LEDBlue  = LEDState{B: 255, Brightness: 255}
	LEDDark  = LEDState{}
)

func (s LEDState) String() string {
	if s.R == 0 && s.G == 0 && s.B == 0 {
		return "off"
	}
	c := fmt.Sprintf("#%02x%02x%02x", s.R, s.G, s.B)
	switch c {
	case "#ffffff":
		c = "white"
	case "#0000ff":
		c = "blue"
	}
	if s.Brightness != 255 {
		c += fmt.Sprintf("@%d%%", int(s.Brightness)*100/255)
	}
	if s.Blink != "" {
		c += " " + s.Blink
	}
	return c
}

// LED is one /sys/class/leds entry.
type LED struct {
	Name, Color, Func string
	Max               int
}

// statusLEDs picks the LEDs that show device status: "*:status*", else
// "*:power*", else "*:system*", else all.
func statusLEDs(all []LED) []LED {
	for _, f := range []string{"status", "power", "system"} {
		var out []LED
		for _, l := range all {
			if strings.HasPrefix(l.Func, f) {
				out = append(out, l)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return all
}

// parseLEDs reads "name max" lines.
func parseLEDs(out string) []LED {
	var leds []LED
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		max, _ := strconv.Atoi(f[1])
		if max <= 0 {
			max = 1
		}
		l := LED{Name: f[0], Max: max}
		if c, fn, ok := strings.Cut(f[0], ":"); ok {
			l.Color, l.Func = c, fn
			if i := strings.Index(fn, ":"); i >= 0 { // devicename:color:function
				l.Color, l.Func = fn[:i], fn[i+1:]
			}
		}
		leds = append(leds, l)
	}
	sort.Slice(leds, func(i, j int) bool { return leds[i].Name < leds[j].Name })
	return leds
}

// channel: how much of this LED's colour the state asks for (0-255), and
// whether this LED takes part in the white phase of Alternate.
func channel(l LED, s LEDState, hasRGB, hasColor map[string]bool) (int, bool) {
	switch {
	case hasRGB[l.Func]: // an RGB group: each LED is one channel
		switch l.Color {
		case "red":
			return int(s.R), true
		case "green":
			return int(s.G), true
		case "blue":
			return int(s.B), false
		}
		return 0, false
	}
	// single-colour LEDs: light the ones of the wanted colour, else any
	white := s.R > 0 && s.G > 0 && s.B > 0
	switch {
	case white && hasColor["white"]:
		return map[bool]int{true: 255}[l.Color == "white"], l.Color == "white"
	case !white && s.B > 0 && hasColor["blue"]:
		return map[bool]int{true: 255}[l.Color == "blue"], false
	case s.R == 0 && s.G == 0 && s.B == 0:
		return 0, false
	}
	return 255, false
}

// ledScript renders a state onto the given LEDs.
func ledScript(leds []LED, s LEDState) string {
	groups := map[string]map[string]bool{}
	hasColor := map[string]bool{}
	for _, l := range leds {
		if groups[l.Func] == nil {
			groups[l.Func] = map[string]bool{}
		}
		groups[l.Func][l.Color] = true
		hasColor[l.Color] = true
	}
	hasRGB := map[string]bool{}
	for fn, cs := range groups {
		hasRGB[fn] = cs["red"] && cs["green"] && cs["blue"]
	}
	var b strings.Builder
	for _, l := range leds {
		v, whitePart := channel(l, s, hasRGB, hasColor)
		v = v * int(s.Brightness) / 255 * l.Max / 255
		p := "/sys/class/leds/" + l.Name
		on, off := 0, 0
		switch {
		case v == 0 && !(s.Blink == Alternate && whitePart):
		case s.Blink == Flash:
			on, off = 500, 500
		case s.Blink == FastFlash:
			on, off = 150, 150
		case s.Blink == Alternate && whitePart:
			// red+green blink over a steady blue: white <-> blue
			if v == 0 {
				v = l.Max * int(s.Brightness) / 255
			}
			on, off = 500, 500
		}
		if on > 0 && v > 0 {
			// brightness first: the timer blinks at the level it finds
			fmt.Fprintf(&b, "echo none > %[1]s/trigger; echo %[2]d > %[1]s/brightness; echo timer > %[1]s/trigger; echo %[3]d > %[1]s/delay_on; echo %[4]d > %[1]s/delay_off\n", p, v, on, off)
		} else {
			fmt.Fprintf(&b, "echo none > %[1]s/trigger; echo %[2]d > %[1]s/brightness\n", p, v)
		}
	}
	return b.String()
}

// LEDs lists the AP's status LEDs.
func (cl *Client) LEDs() ([]LED, error) {
	out, err := cl.Run(`for l in /sys/class/leds/*; do [ -e "$l/brightness" ] && echo "${l##*/} $(cat $l/max_brightness 2>/dev/null)"; done`)
	if err != nil {
		return nil, err
	}
	leds := statusLEDs(parseLEDs(string(out)))
	if len(leds) == 0 {
		return nil, fmt.Errorf("no LEDs found")
	}
	return leds, nil
}

// ShowLED puts a state on the AP's status LEDs.
func (cl *Client) ShowLED(s LEDState) error {
	leds, err := cl.LEDs()
	if err != nil {
		return err
	}
	_, err = cl.Run(ledScript(leds, s))
	return err
}
