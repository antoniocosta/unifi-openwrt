// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"math"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Optional idle effect for APs with RGB status LEDs (e.g. the OnHub's ring
// of six): instead of steady blue (adopted, running), fade from blue into a
// slowly turning rainbow, one hue per LED group. Off by default (-led-idle);
// every other LED state (white, flashing, Locate, off) is unchanged. Runs
// only when the daemon is on the AP itself: it writes the LED brightness
// files directly, about 12 times a second, and only the values that change.

// IsLocal: the daemon runs on the AP (Target.Addr "local").
func (cl *Client) IsLocal() bool { return cl.t.Addr == "local" }

// rgbGroup is one RGB LED: its red, green and blue channels.
type rgbGroup struct {
	name    string
	r, g, b LED
}

func rgbGroups(leds []LED) []rgbGroup {
	by := map[string]*rgbGroup{}
	for _, l := range leds {
		g := by[l.Func]
		if g == nil {
			g = &rgbGroup{name: l.Func}
			by[l.Func] = g
		}
		switch l.Color {
		case "red":
			g.r = l
		case "green":
			g.g = l
		case "blue":
			g.b = l
		}
	}
	var out []rgbGroup
	for _, g := range by {
		if g.r.Name != "" && g.g.Name != "" && g.b.Name != "" {
			out = append(out, *g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// hueRGB: fully saturated colour for hue h (degrees), 0-1 per channel.
func hueRGB(h float64) (float64, float64, float64) {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	x := 1 - math.Abs(math.Mod(h/60, 2)-1)
	switch int(h / 60) {
	case 0:
		return 1, x, 0
	case 1:
		return x, 1, 0
	case 2:
		return 0, 1, x
	case 3:
		return 0, x, 1
	case 4:
		return x, 0, 1
	default:
		return 1, 0, x
	}
}

// rainbowFrame: the colour (0-1) of group i of n at time t: a rainbow
// around the ring turning once per period, blended in from blue over fade.
func rainbowFrame(i, n int, t, period, fade time.Duration) (float64, float64, float64) {
	h := 240 + 360*t.Seconds()/period.Seconds() + 360*float64(i)/float64(n)
	r, g, b := hueRGB(h)
	a := 1.0
	if fade > 0 && t < fade {
		a = t.Seconds() / fade.Seconds()
		a = a * a * (3 - 2*a) // smooth start and end
	}
	return a * r, a * g, (1-a)*1 + a*b
}

// Rainbow is a running idle effect.
type Rainbow struct {
	stop chan struct{}
	done sync.WaitGroup
}

// StartRainbow animates the RGB groups among leds at brightness br (0-255);
// nil when there are fewer than two RGB groups.
func StartRainbow(leds []LED, br uint8, period, fade time.Duration) *Rainbow {
	groups := rgbGroups(leds)
	if len(groups) < 2 {
		return nil
	}
	rb := &Rainbow{stop: make(chan struct{})}
	rb.done.Add(1)
	go func() {
		defer rb.done.Done()
		last := map[string]int{}
		set := func(l LED, v float64) {
			n := int(math.Round(v * float64(br) / 255 * float64(l.Max)))
			if old, ok := last[l.Name]; ok && old == n {
				return
			}
			last[l.Name] = n
			_ = os.WriteFile("/sys/class/leds/"+l.Name+"/brightness", []byte(strconv.Itoa(n)), 0)
		}
		for _, g := range groups {
			for _, l := range []LED{g.r, g.g, g.b} {
				_ = os.WriteFile("/sys/class/leds/"+l.Name+"/trigger", []byte("none"), 0)
			}
		}
		t0 := time.Now()
		tick := time.NewTicker(80 * time.Millisecond)
		defer tick.Stop()
		for {
			t := time.Since(t0)
			for i, g := range groups {
				r, gr, b := rainbowFrame(i, len(groups), t, period, fade)
				set(g.r, r)
				set(g.g, gr)
				set(g.b, b)
			}
			select {
			case <-rb.stop:
				return
			case <-tick.C:
			}
		}
	}()
	return rb
}

// Stop ends the effect (the caller then shows the next state).
func (rb *Rainbow) Stop() {
	if rb == nil {
		return
	}
	close(rb.stop)
	rb.done.Wait()
}
