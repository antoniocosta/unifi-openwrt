// SPDX-License-Identifier: GPL-2.0-or-later

package inform

// Profile is what a UniFi model reports about its capabilities. The
// controller gates features on these (WPA3, 80 MHz, mesh, ...), so a device
// that wants a model's behaviour reports that model's values, and the
// translator clamps whatever the real hardware cannot do.
type Profile struct {
	Model, Display, Version string
	FWCaps, FW2Caps         int64
	WifiCaps, WifiCaps2     int64
	Radios                  map[string]RadioProfile // by band: "ng", "na"
}

type RadioProfile struct {
	IEEEModes, RadioCaps, RadioCaps2 int64
	Is11ac, HasDFS, HasFCCDFS        bool
	MinTx, MaxTx, NSS                int
}

// U7PG2 is a real UAP-AC-Pro on 6.8.2.15592, as recorded in open-unifi's
// test harness (internal/server/zz_minimaldiff_scratch_test.go, modelled on
// their bench AP). Decoded in docs/internals.md.
var U7PG2 = Profile{
	Model: U7PG2Model, Display: U7PG2ModelDisplay, Version: U7PG2Version,
	FWCaps: 3892247871, FW2Caps: 1208549376,
	WifiCaps: 559857373, WifiCaps2: 48,
	Radios: map[string]RadioProfile{
		"ng": {IEEEModes: 10, RadioCaps: 16420, RadioCaps2: 27, MinTx: 6, MaxTx: 22, NSS: 3},
		"na": {IEEEModes: 21, RadioCaps: 50479140, RadioCaps2: 27, Is11ac: true, HasDFS: true, HasFCCDFS: true, MinTx: 6, MaxTx: 22, NSS: 3},
	},
}
