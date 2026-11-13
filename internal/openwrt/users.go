// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"fmt"
	"strings"

	"github.com/antoniocosta/unifi-openwrt/internal/translate"
)

// usersScript keeps /etc/passwd and /etc/shadow in line with the controller's
// device logins. stdin: one "name:hash" per line. It only touches the users
// it added itself (listed in /etc/unifi-openwrt/users); an existing account
// it does not manage is never changed. Logins are uid 0 (UniFi's device
// login is an admin login), shell /bin/ash.
const usersScript = `set -e
M=/etc/unifi-openwrt/users; mkdir -p /etc/unifi-openwrt; touch $M; chmod 600 $M
want=" "
while IFS=: read -r n h; do
  [ -n "$n" ] || continue
  want="$want$n "
  if grep -q "^$n:" /etc/passwd && ! grep -qx "$n" $M; then echo "skip $n: existing account"; continue; fi
  grep -q "^$n:" /etc/passwd || echo "$n:x:0:0:UniFi device admin:/root:/bin/ash" >> /etc/passwd
  cur=$(grep "^$n:" /etc/shadow | cut -d: -f2 || true)
  if [ "$cur" != "$h" ]; then
    grep -v "^$n:" /etc/shadow > /etc/shadow.uow || true
    echo "$n:$h:$(( $(date +%s) / 86400 )):0:99999:7:::" >> /etc/shadow.uow
    chmod 600 /etc/shadow.uow; mv /etc/shadow.uow /etc/shadow
    echo "set $n"
  fi
  grep -qx "$n" $M || echo "$n" >> $M
done
for n in $(cat $M); do
  case "$want" in *" $n "*) continue;; esac
  grep -v "^$n:" /etc/passwd > /etc/passwd.uow; mv /etc/passwd.uow /etc/passwd; chmod 644 /etc/passwd
  grep -v "^$n:" /etc/shadow > /etc/shadow.uow; chmod 600 /etc/shadow.uow; mv /etc/shadow.uow /etc/shadow
  grep -vx "$n" $M > $M.uow || true; mv $M.uow $M
  echo "removed $n"
done
echo done`

// SyncUsers makes the AP's extra admin logins match users. It returns what
// it changed (names only; hashes never leave stdin).
func (cl *Client) SyncUsers(users []translate.User) ([]string, error) {
	var in strings.Builder
	for _, u := range users {
		if strings.ContainsAny(u.Name+u.Hash, ": \t\n\r") {
			return nil, fmt.Errorf("bad user entry %q", u.Name) // validated in translate.Users too
		}
		fmt.Fprintf(&in, "%s:%s\n", u.Name, u.Hash)
	}
	out, err := cl.RunStdin("sh -c "+shQuote(usersScript), []byte(in.String()))
	if err != nil {
		return nil, fmt.Errorf("users: %v", err)
	}
	var changed []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "done" && l != "" {
			changed = append(changed, l)
		}
	}
	if !strings.Contains(string(out), "done") {
		return changed, fmt.Errorf("users: unexpected output")
	}
	return changed, nil
}
