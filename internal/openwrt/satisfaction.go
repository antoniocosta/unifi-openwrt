// SPDX-License-Identifier: GPL-2.0-or-later

package openwrt

import (
	"sync"
	"time"
)

// Wi-Fi experience ("satisfaction"), the way UAP firmware 6.8.2 computes it
// (docs/internals.md: read from libubnt's calc_sta_satisfaction and mcad in the
// public firmware). Per client and cycle, sub-scores 0..100:
//
//	PHY rate (anomaly bit 2, 5 GHz clients only; 2.4 GHz is always 100):
//	  eff = tx_rate * avail / 100, avail = min(100, 100 + cu_self_rx + cu_self_tx - cu_total)
//	  <1 Mbps 0; 1-10 -> 60-80; 10-15 -> 80-90; 15-25 -> 90-95; 25-150 -> 95-100; above 100
//	Wi-Fi retries (bit 16): needs >= 30 tx attempts in the cycle;
//	  pct = 100*retries/attempts; <= 25 -> 100, else 100 - 51*(pct-25)/100
//
// Each sub-score is averaged over the current and up to 4 earlier cycles;
// satisfaction_real = the lowest average, anomalies = bits of averages <= 79,
// and the reported satisfaction is satisfaction_real limited to 60..100.
// Signal strength is not an input. Not computed here: TCP/DNS/ICMP latency,
// DNS timeouts, disconnects, DHCP/ARP (they need traffic inspection), and the
// chip-specific drop score.

const (
	satCycle       = 5 * time.Second // the firmware's cycle length is not known
	anomalyPHYRate = 2
	anomalyRetries = 16
)

// SatResult is one client's score.
type SatResult struct {
	Reported  int // satisfaction: Real limited to 60..100
	Real      int // satisfaction_real
	Now       int // satisfaction_now: this cycle only
	Anomalies int // bits of sub-scores averaging <= 79
}

type satState struct {
	last        time.Time
	phy, retry  []int // newest last, at most 5
	pk, retries int64 // tx counters at the last cycle
	res         SatResult
}

var (
	satMu     sync.Mutex
	satStates = map[string]*satState{}
)

func phyScore(txRateKbps, avail int, is5 bool) int {
	if txRateKbps <= 0 || !is5 {
		return 100
	}
	m := float64(txRateKbps) * float64(avail) / 100 / 1000 // effective Mbps
	switch {
	case m < 1:
		return 0
	case m < 10:
		return int(60 + (m-1)/9*20)
	case m < 15:
		return int(80 + (m-10)/5*10)
	case m < 25:
		return int(90 + (m-15)/10*5)
	case m < 150:
		return int(95 + (m-25)/125*5)
	}
	return 100
}

func retryScore(retries, attempts int64) int {
	if attempts < 30 {
		return 100
	}
	pct := 100 * retries / attempts
	if pct <= 25 {
		return 100
	}
	return int(100 - 51*(pct-25)/100)
}

func avgInt(v []int) int {
	if len(v) == 0 {
		return 100
	}
	t := 0
	for _, x := range v {
		t += x
	}
	return t / len(v)
}

// staSatisfaction scores one client. avail: airtime left by others on its
// radio (percent). A new cycle starts every satCycle; in between the last
// result stands.
func staSatisfaction(st Station, is5 bool, avail int, now time.Time) SatResult {
	satMu.Lock()
	defer satMu.Unlock()
	s := satStates[st.MAC]
	if s == nil {
		s = &satState{pk: st.TxPackets, retries: st.TxRetries}
		satStates[st.MAC] = s
	} else if now.Sub(s.last) < satCycle {
		return s.res
	}
	dR, dP := st.TxRetries-s.retries, st.TxPackets-s.pk
	if dR < 0 || dP < 0 { // the client re-associated: counters restarted
		dR, dP = 0, 0
	}
	s.retries, s.pk, s.last = st.TxRetries, st.TxPackets, now
	phy, retry := phyScore(st.TxRate, avail, is5), retryScore(dR, dP+dR)
	s.phy, s.retry = append(s.phy, phy), append(s.retry, retry)
	if len(s.phy) > 5 {
		s.phy, s.retry = s.phy[1:], s.retry[1:]
	}
	aPhy, aRetry := avgInt(s.phy), avgInt(s.retry)
	r := SatResult{Real: min(aPhy, aRetry), Now: min(phy, retry)}
	if aPhy <= 79 {
		r.Anomalies |= anomalyPHYRate
	}
	if aRetry <= 79 {
		r.Anomalies |= anomalyRetries
	}
	r.Reported = satClamp(r.Real)
	s.res = r
	return r
}

func satClamp(v int) int { return max(60, min(100, v)) }

// satPrune forgets clients not scored for 10 minutes.
func satPrune(now time.Time) {
	satMu.Lock()
	defer satMu.Unlock()
	for m, s := range satStates {
		if now.Sub(s.last) > 10*time.Minute {
			delete(satStates, m)
		}
	}
}
