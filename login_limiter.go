package main

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Brute-force protection for the login endpoints. Failures are tracked per
// source IP + username (escalating lock after a few consecutive failures) and
// per source IP across all usernames (guards against username spraying).
// State is in memory only and resets when the server restarts.
const (
	loginPairMaxFailures = 3
	loginPairResetAfter  = time.Hour
	loginIPMaxFailures   = 10
	loginIPWindow        = 5 * time.Minute
	loginIPLockDuration  = 5 * time.Minute
	genericLoginError    = "Invalid login credentials or OTP"
)

var loginPairLockDurations = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

type loginPairState struct {
	failures    int
	level       int
	lockedUntil time.Time
	lastFailure time.Time
}

type loginIPState struct {
	failureTimes []time.Time
	lockedUntil  time.Time
}

type loginLimiter struct {
	mu    sync.Mutex
	pairs map[string]*loginPairState
	ips   map[string]*loginIPState
	now   func() time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{pairs: make(map[string]*loginPairState), ips: make(map[string]*loginIPState), now: time.Now}
}

func loginPairKey(ip, username string) string { return ip + "|" + canonicalUsername(username) }

// pairLocked reports the remaining lock time for an IP+username pair, resetting
// the escalation level once the pair has been quiet long enough. Caller holds mu.
func (l *loginLimiter) pairLocked(key string, now time.Time) time.Duration {
	state, ok := l.pairs[key]
	if !ok {
		return 0
	}
	if now.Before(state.lockedUntil) {
		return state.lockedUntil.Sub(now)
	}
	if now.Sub(state.lastFailure) >= loginPairResetAfter {
		delete(l.pairs, key)
	}
	return 0
}

// ipLocked reports the remaining lock time for a source IP. Caller holds mu.
func (l *loginLimiter) ipLocked(ip string, now time.Time) time.Duration {
	if state, ok := l.ips[ip]; ok && now.Before(state.lockedUntil) {
		return state.lockedUntil.Sub(now)
	}
	return 0
}

// check must run before any credential evaluation so a locked request never
// reveals whether a guess would have succeeded.
func (l *loginLimiter) check(ip, username string) (time.Duration, bool) {
	if l == nil {
		return 0, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	wait := l.pairLocked(loginPairKey(ip, username), now)
	if ipWait := l.ipLocked(ip, now); ipWait > wait {
		wait = ipWait
	}
	return wait, wait > 0
}

// fail records a failed attempt and returns the lock duration it triggered, if any.
func (l *loginLimiter) fail(ip, username string) time.Duration {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := loginPairKey(ip, username)
	l.pairLocked(key, now) // drops a stale pair so escalation starts over
	pair, ok := l.pairs[key]
	if !ok {
		pair = &loginPairState{}
		l.pairs[key] = pair
	}
	pair.failures++
	pair.lastFailure = now
	var lockedFor time.Duration
	if pair.failures >= loginPairMaxFailures {
		lockedFor = loginPairLockDurations[pair.level]
		pair.lockedUntil = now.Add(lockedFor)
		pair.failures = 0
		if pair.level < len(loginPairLockDurations)-1 {
			pair.level++
		}
	}

	ipState, ok := l.ips[ip]
	if !ok {
		ipState = &loginIPState{}
		l.ips[ip] = ipState
	}
	ipState.failureTimes = append(pruneLoginFailures(ipState.failureTimes, now), now)
	if len(ipState.failureTimes) >= loginIPMaxFailures {
		ipState.lockedUntil = now.Add(loginIPLockDuration)
		ipState.failureTimes = nil
		if loginIPLockDuration > lockedFor {
			lockedFor = loginIPLockDuration
		}
	}
	return lockedFor
}

// success clears the IP+username pair. The per-IP history is kept so a valid
// account cannot be used to reset the IP-wide counter.
func (l *loginLimiter) success(ip, username string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.pairs, loginPairKey(ip, username))
	l.mu.Unlock()
}

func pruneLoginFailures(times []time.Time, now time.Time) []time.Time {
	kept := times[:0]
	for _, t := range times {
		if now.Sub(t) < loginIPWindow {
			kept = append(kept, t)
		}
	}
	return kept
}

func (l *loginLimiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for key := range l.pairs {
		l.pairLocked(key, now)
	}
	for ip, state := range l.ips {
		state.failureTimes = pruneLoginFailures(state.failureTimes, now)
		if len(state.failureTimes) == 0 && !now.Before(state.lockedUntil) {
			delete(l.ips, ip)
		}
	}
}

func (l *loginLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.prune()
	}
}

func sendLoginLocked(w http.ResponseWriter, wait time.Duration) {
	seconds := int(math.Ceil(wait.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	sendJSONError(w, http.StatusTooManyRequests, fmt.Sprintf("Too many failed attempts. Try again in %d seconds.", seconds))
}
