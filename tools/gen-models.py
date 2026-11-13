#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Generate internal/models/catalog.json (UniFi AP models) from unifi-emu's
model_profiles.json (MIT, Copyright (c) 2026 James Braid; see
internal/models/NOTICE). Run: python3 -I tools/gen-models.py <path to a unifi-emu checkout>"""
import json, sys

src = sys.argv[1]
d = json.load(open(src + "/model_profiles.json"))

def gen(display):
    s = display.upper()
    if s.startswith("U7-") or s.startswith("U7 ") or " BE" in s or "E7" in s:
        return 7
    if s.startswith("U6") or "WIFI 6" in s or s.startswith("UAP-6") or "AX" in s:
        return 6
    if "XG" in s or "ULTRA" in s or "AC" in s or "HD" in s or "SHD" in s or "NANOHD" in s or "FLEXHD" in s or "BEACONHD" in s or "IW HD" in s:
        return 5
    return 4

out = []
for m in d["models"]:
    if str(m.get("type", "")).lower() != "uap":
        continue
    radios = [{"band": r["radio"], "nss": r.get("nss", 2), "min_tx": r.get("min_txpower", 0),
               "max_tx": r.get("max_txpower", 0)} for r in m.get("radios", [])]
    if not radios:
        continue
    disp = m.get("model_display") or m["model"]
    g = gen(disp)
    if disp == m["model"] and m["model"].startswith("UAPA"):
        g = 0  # codename only: generation unknown, not suggested
    out.append({"model": m["model"], "display": disp, "version": m.get("version", ""),
                "gen": g, "radios": radios})
# corrections from measured devices (docs/internals.md: the real AC-Pro is 3x3)
FIX_NSS = {"U7PG2": 3}
for m in out:
    if m["model"] in FIX_NSS:
        for r in m["radios"]:
            r["nss"] = FIX_NSS[m["model"]]
out.sort(key=lambda x: x["model"])
json.dump({"source": "unifi-emu model_profiles.json (MIT, Copyright (c) 2026 James Braid)",
           "controller_version": d.get("controller_version"), "models": out},
          open("internal/models/catalog.json", "w"), indent=1)
print(len(out), "models")
