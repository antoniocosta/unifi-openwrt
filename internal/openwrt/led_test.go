// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const onhubLEDs = `blue:status-0 255
blue:status-1 255
green:status-0 255
green:status-1 255
red:status-0 255
red:status-1 255
`

func TestLEDScriptRGB(t *testing.T) {
	leds := statusLEDs(parseLEDs(onhubLEDs))
	if len(leds) != 6 {
		t.Fatalf("leds %v", leds)
	}
	sc := ledScript(leds, LEDBlue)
	if !strings.Contains(sc, "echo 255 > /sys/class/leds/blue:status-0/brightness") ||
		!strings.Contains(sc, "echo 0 > /sys/class/leds/red:status-0/brightness") {
		t.Errorf("blue:\n%s", sc)
	}
	sc = ledScript(leds, LEDWhite)
	for _, c := range []string{"red", "green", "blue"} {
		if !strings.Contains(sc, "echo 255 > /sys/class/leds/"+c+":status-1/brightness") {
			t.Errorf("white needs %s:\n%s", c, sc)
		}
	}
	sc = ledScript(leds, LEDState{R: 255, G: 128, Brightness: 128})
	if !strings.Contains(sc, "echo 128 > /sys/class/leds/red:status-0/brightness") ||
		!strings.Contains(sc, "echo 64 > /sys/class/leds/green:status-0/brightness") {
		t.Errorf("orange at 50%%:\n%s", sc)
	}
	alt := LEDBlue
	alt.Blink = Alternate
	sc = ledScript(leds, alt)
	if !strings.Contains(sc, "echo timer > /sys/class/leds/red:status-0/trigger") ||
		strings.Contains(sc, "echo timer > /sys/class/leds/blue:status-0/trigger") {
		t.Errorf("alternate: red/green blink over steady blue:\n%s", sc)
	}
	if err := exec.Command("sh", "-n", "-c", sc).Run(); err != nil {
		t.Errorf("sh -n: %v", err)
	}
}

func TestLEDScriptSingleColour(t *testing.T) {
	leds := statusLEDs(parseLEDs("white:status 1\nblue:status 1\ngreen:wlan 1\n"))
	if len(leds) != 2 {
		t.Fatalf("status LEDs %v", leds)
	}
	sc := ledScript(leds, LEDWhite)
	if !strings.Contains(sc, "echo 1 > /sys/class/leds/white:status/brightness") ||
		!strings.Contains(sc, "echo 0 > /sys/class/leds/blue:status/brightness") {
		t.Errorf("white:\n%s", sc)
	}
	f := LEDBlue
	f.Blink = FastFlash
	sc = ledScript(leds, f)
	if !strings.Contains(sc, "echo timer > /sys/class/leds/blue:status/trigger") {
		t.Errorf("locate:\n%s", sc)
	}
	// a device with only a green power LED still shows something
	leds = statusLEDs(parseLEDs("green:power 255\n"))
	if sc := ledScript(leds, LEDBlue); !strings.Contains(sc, "echo 255 > /sys/class/leds/green:power/brightness") {
		t.Errorf("fallback:\n%s", sc)
	}
}

func TestRainbow(t *testing.T) {
	leds := parseLEDs("red:status-0 255\ngreen:status-0 255\nblue:status-0 255\nred:status-1 255\ngreen:status-1 255\nblue:status-1 255\nath10k-phy0 1")
	if g := rgbGroups(leds); len(g) != 2 || g[0].name != "status-0" {
		t.Fatalf("groups %+v", g)
	}
	// starts blue, fully rainbow after the fade
	r, g, b := rainbowFrame(1, 2, 0, 20*time.Second, 3*time.Second)
	if r != 0 || g != 0 || b != 1 {
		t.Fatalf("t=0: %v %v %v", r, g, b)
	}
	r, g, b = rainbowFrame(0, 2, 3*time.Second, 30*time.Second, 3*time.Second)
	if hi, lo := math.Max(math.Max(r, g), b), math.Min(math.Min(r, g), b); hi < 0.99 || lo > 0.01 {
		t.Fatalf("not saturated: %v %v %v", r, g, b)
	}
	if r, g, b := hueRGB(0); r != 1 || g != 0 || b != 0 {
		t.Fatal("red")
	}
}
