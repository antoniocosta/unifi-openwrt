// SPDX-License-Identifier: GPL-2.0-or-later

package translate

import "strings"

// ACLsFromUCI is the MAC filter UCI holds for each of sections, in the form
// of Result.ACLs. A section with a macfile is left out, as Plan leaves it
// out of Result.ACLs.
func ACLsFromUCI(cur UCI, sections []string) []ACL {
	var out []ACL
	for _, s := range sections {
		if cur["wireless."+s+".macfile"] != "" {
			continue
		}
		a := ACL{Section: s}
		if p := cur["wireless."+s+".macfilter"]; p == "deny" || p == "allow" {
			a.Policy = p
			for _, f := range strings.Fields(cur["wireless."+s+".maclist"]) {
				if m := strings.ToLower(strings.Trim(f, "'")); m != "" {
					a.MACs = append(a.MACs, m)
				}
			}
		}
		out = append(out, a)
	}
	return out
}
