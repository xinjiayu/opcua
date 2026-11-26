package server

import (
	"context"
	"testing"
	"time"
)

// P7a: the unit of the session timeout.
//
// This is a defect of its own and not a part of P7, although P7 depends on
// it: the expiry sweep of P7 compares the time of the last request with the
// timeout stored for the session.
//
// Upstream v0.8.0 (server/session_service.go) wrote the bounds of the session
// timeout as plain millisecond numbers and compared them with a
// time.Duration, which counts nanoseconds. Every requested timeout above
// 1.8 ms counted as out of range and was replaced by a "default" of 60 000
// nanoseconds, 60 microseconds, which the CreateSession response reported to
// the client as a RevisedSessionTimeout of 0.
//
// What the client sees changes with the fix: RevisedSessionTimeout goes from
// 0 ms to the timeout the server actually grants. That corrects a wrong
// answer. No behaviour of the server depended on the old value, because
// nothing read it.
//
// The helpers this test uses live in patch_p7_session_limit_test.go.

func TestCreateSessionRevisedTimeout(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)

	// the server keeps what the client asked for while it stays inside
	// 100 ms to 1 h and falls back to 60 s otherwise. The bounds are the ones
	// upstream chose when it fixed the unit error in v0.9.3; the v0.8.0 based
	// branch of this fork capped the timeout at 30 min.
	cases := []struct {
		name      string
		requested time.Duration
		want      time.Duration
	}{
		{"ordinary", 120 * time.Second, 120 * time.Second},
		{"short", 500 * time.Millisecond, 500 * time.Millisecond},
		{"at-minimum", 100 * time.Millisecond, 100 * time.Millisecond},
		{"at-maximum", time.Hour, time.Hour},
		{"zero", 0, 60 * time.Second},
		{"negative", -time.Second, 60 * time.Second},
		{"below-minimum", 50 * time.Millisecond, 60 * time.Second},
		{"above-maximum", 2 * time.Hour, 60 * time.Second},
		{"single-millisecond", time.Millisecond, 60 * time.Second},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := connectWithTimeout(t, addr, c.requested)
			t.Cleanup(func() { cl.Close(context.Background()) })

			if got := cl.Session().RevisedTimeout(); got != c.want {
				t.Errorf("the client was told the session timeout is %v, want %v", got, c.want)
			}

			// and the server works with the same value, not with the
			// 60 microseconds it stored upstream
			s.sb.mu.Lock()
			defer s.sb.mu.Unlock()
			var stored []time.Duration
			for _, sess := range s.sb.s {
				stored = append(stored, sess.cfg.sessionTimeout)
			}
			if len(stored) != 1 || stored[0] != c.want {
				t.Errorf("the server stored the session timeouts %v, want exactly one of %v", stored, c.want)
			}
		})
	}
}
