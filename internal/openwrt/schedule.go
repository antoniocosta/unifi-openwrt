// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"unifi-openwrt/internal/translate"
)

// scheduleScript runs every minute from cron on the AP: for each scheduled
// wifi-iface it works out whether it should be up now (local time, the
// timezone the controller set) and flips `disabled` + `wifi reload` when
// needed. It keeps working when the daemon is away, like a UniFi AP.
const scheduleScript = `#!/bin/sh
C=/etc/unifi-openwrt/schedule.conf
[ -s "$C" ] || exit 0
d=$(date +%a | tr A-Z a-z); t=$(date +%H%M)
changed=0
for l in $(awk -v d="$d" -v t="$t" '
  { if (!($1 in mode)) mode[$1] = $2
    if ($3 == d) { a = $4 + 0; b = $5 + 0; tt = t + 0
      if (a <= b ? (tt >= a && tt < b) : (tt >= a || tt < b)) win[$1] = 1 } }
  END { for (s in mode) { up = (mode[s] == "on") ? (s in win) : !(s in win); print s ":" up } }' "$C"); do
  sec=${l%%:*}; up=${l##*:}
  want=1; [ "$up" = 1 ] && want=0
  cur=$(uci -q get wireless.$sec.disabled || echo 0)
  if [ "$cur" != "$want" ]; then
    uci set wireless.$sec.disabled=$want; changed=1
    logger -t unifi-openwrt "schedule: $sec $( [ $want = 0 ] && echo on || echo off )"
  fi
done
[ $changed = 1 ] && { uci commit wireless; wifi reload; }
exit 0
`

var sectionOK = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// scheduleConf renders schedule.conf: "<section> <on|off> <day> <start> <end>",
// plus "<section> <mode> - 0 0" so a WLAN with no window today is known.
func scheduleConf(ss []translate.Schedule) (string, error) {
	var lines []string
	for _, s := range ss {
		if !sectionOK.MatchString(s.Section) {
			return "", fmt.Errorf("bad section %q", s.Section)
		}
		mode := "off"
		if s.OnDuring {
			mode = "on"
		}
		lines = append(lines, fmt.Sprintf("%s %s - 0 0", s.Section, mode))
		var days []string
		for d := range s.Windows {
			days = append(days, d)
		}
		sort.Strings(days)
		for _, d := range days {
			for _, w := range s.Windows[d] {
				a, b, _ := strings.Cut(w, "-")
				lines = append(lines, fmt.Sprintf("%s %s %s %s %s", s.Section, mode, d, a, b))
			}
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// SyncSchedules installs (or removes) the WLAN schedules and the cron job,
// then runs the script once.
func (cl *Client) SyncSchedules(ss []translate.Schedule) error {
	conf, err := scheduleConf(ss)
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`set -e
mkdir -p /etc/unifi-openwrt
cat > /etc/unifi-openwrt/schedule.sh.new <<'SCRIPT'
%sSCRIPT
chmod 755 /etc/unifi-openwrt/schedule.sh.new
mv /etc/unifi-openwrt/schedule.sh.new /etc/unifi-openwrt/schedule.sh
printf '%%s' %s > /etc/unifi-openwrt/schedule.conf
touch /etc/crontabs/root
old=$(cat /etc/crontabs/root)
grep -v '# unifi-openwrt schedule' /etc/crontabs/root > /etc/crontabs/root.uow || true
[ -s /etc/unifi-openwrt/schedule.conf ] && echo '* * * * * /etc/unifi-openwrt/schedule.sh # unifi-openwrt schedule' >> /etc/crontabs/root.uow
mv /etc/crontabs/root.uow /etc/crontabs/root
[ "$old" = "$(cat /etc/crontabs/root)" ] && pgrep crond >/dev/null || /etc/init.d/cron restart
/etc/unifi-openwrt/schedule.sh
echo ok`, scheduleScript, shQuote(conf))
	out, err := cl.Run(script)
	if err != nil {
		return fmt.Errorf("schedules: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
