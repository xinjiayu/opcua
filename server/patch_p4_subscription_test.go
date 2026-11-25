package server

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P4: the subscription service.
//
// (a) CreateSubscription accepted a request whose session the server does not
//     know, stored a nil session and started the publishing goroutine anyway.
//     A few ticks later the keepalive path dereferenced that session, in a
//     goroutine nobody recovers: process dead.
// (b) CreateSubscription took the requested publishing interval as it was and
//     handed it to time.NewTicker. Zero, negative, NaN or overflowing values
//     panic inside NewTicker, again in that goroutine: process dead.
// (c) DeleteSubscriptions dereferenced the session before checking anything.
// (d) The publishing goroutine had no recover, so any panic inside it ended
//     the process instead of the subscription.

// unknownToken is an authentication token no session was ever created for.
func unknownToken() *ua.NodeID {
	return ua.NewNumericNodeID(0, 4711)
}

// subscriptionIDs returns the ids of the subscriptions the service currently
// holds.
func subscriptionIDs(svc *SubscriptionService) []uint32 {
	svc.Mu.Lock()
	defer svc.Mu.Unlock()

	ids := make([]uint32, 0, len(svc.Subs))
	for id := range svc.Subs {
		ids = append(ids, id)
	}
	return ids
}

// hasSubscription reports whether the service still holds the subscription.
func hasSubscription(svc *SubscriptionService, id uint32) bool {
	svc.Mu.Lock()
	defer svc.Mu.Unlock()

	_, ok := svc.Subs[id]
	return ok
}

// createSubscription registers a subscription for sess and returns its id.
func createSubscription(t *testing.T, s *Server, sess *session, interval float64) uint32 {
	t.Helper()

	resp := call(t, s, id.CreateSubscriptionRequest_Encoding_DefaultBinary, &ua.CreateSubscriptionRequest{
		RequestHeader:               header(sess),
		RequestedPublishingInterval: interval,
		RequestedLifetimeCount:      1000,
		RequestedMaxKeepAliveCount:  20,
	})
	cr, ok := resp.(*ua.CreateSubscriptionResponse)
	if !ok {
		t.Fatalf("expected *ua.CreateSubscriptionResponse, got %T", resp)
	}
	return cr.SubscriptionID
}

func TestCreateSubscriptionRejectsUnknownSession(t *testing.T) {
	crashProbe(t, "unknown-session", func() {
		s := newTestServer(t)
		svc := s.SubscriptionService

		// A 1 ms interval with a keepalive count of 0 sends run() down the
		// keepalive path on its very first tick.
		resp, err := s.handlers[id.CreateSubscriptionRequest_Encoding_DefaultBinary](nil, &ua.CreateSubscriptionRequest{
			RequestHeader:               &ua.RequestHeader{AuthenticationToken: unknownToken()},
			RequestedPublishingInterval: 1,
			RequestedLifetimeCount:      3,
			RequestedMaxKeepAliveCount:  0,
		}, 1)
		time.Sleep(50 * time.Millisecond)

		if err != ua.StatusBadSessionIDInvalid {
			t.Errorf("CreateSubscription with an unknown session returned (%T, %v), want the error %v", resp, err, ua.StatusBadSessionIDInvalid)
		}
		if ids := subscriptionIDs(svc); len(ids) != 0 {
			t.Errorf("CreateSubscription with an unknown session created the subscriptions %v", ids)
		}
	})
}

func TestCreateSubscriptionClampsPublishingInterval(t *testing.T) {
	// The server supports publishing intervals from 100 ms to one hour.
	const (
		minMS = 100.0
		maxMS = 3600000.0
	)

	cases := []struct {
		name      string
		requested float64
		want      float64
	}{
		{name: "zero", requested: 0, want: minMS},
		{name: "negative", requested: -5, want: minMS},
		{name: "nan", requested: math.NaN(), want: minMS},
		{name: "positive-infinity", requested: math.Inf(1), want: maxMS},
		{name: "negative-infinity", requested: math.Inf(-1), want: minMS},
		{name: "below-minimum", requested: 99.999, want: minMS},
		{name: "at-minimum", requested: minMS, want: minMS},
		{name: "ordinary", requested: 1000, want: 1000},
		{name: "fractional", requested: 250.5, want: 250.5},
		{name: "at-maximum", requested: maxMS, want: maxMS},
		{name: "above-maximum", requested: maxMS + 1, want: maxMS},
		{name: "overflows-duration", requested: 1e18, want: maxMS},
	}

	for _, c := range cases {
		crashProbe(t, c.name, func() {
			s := newTestServer(t)
			sess := newTestSession(s)
			svc := s.SubscriptionService

			resp := call(t, s, id.CreateSubscriptionRequest_Encoding_DefaultBinary, &ua.CreateSubscriptionRequest{
				RequestHeader:               header(sess),
				RequestedPublishingInterval: c.requested,
				RequestedLifetimeCount:      1000,
				RequestedMaxKeepAliveCount:  20,
			})
			cr, ok := resp.(*ua.CreateSubscriptionResponse)
			if !ok {
				t.Fatalf("expected *ua.CreateSubscriptionResponse, got %T", resp)
			}

			svc.Mu.Lock()
			sub := svc.Subs[cr.SubscriptionID]
			svc.Mu.Unlock()
			if sub == nil {
				t.Fatalf("subscription %d was not registered", cr.SubscriptionID)
			}

			// the response is the only place where the client learns what
			// the server picked, so it has to carry the revised value too.
			if cr.RevisedPublishingInterval != c.want {
				t.Errorf("response RevisedPublishingInterval = %v ms, want %v ms", cr.RevisedPublishingInterval, c.want)
			}
			if sub.RevisedPublishingInterval != c.want {
				t.Errorf("subscription RevisedPublishingInterval = %v ms, want %v ms", sub.RevisedPublishingInterval, c.want)
			}
		})
	}
}

func TestDeleteSubscriptionsRejectsUnknownSession(t *testing.T) {
	crashProbe(t, "unknown-session", func() {
		s := newTestServer(t)
		svc := s.SubscriptionService
		subID := createSubscription(t, s, newTestSession(s), 3600000)

		resp := call(t, s, id.DeleteSubscriptionsRequest_Encoding_DefaultBinary, &ua.DeleteSubscriptionsRequest{
			RequestHeader:   &ua.RequestHeader{AuthenticationToken: unknownToken()},
			SubscriptionIDs: []uint32{subID, 999999},
		})
		dr, ok := resp.(*ua.DeleteSubscriptionsResponse)
		if !ok {
			t.Fatalf("expected *ua.DeleteSubscriptionsResponse, got %T", resp)
		}

		want := []ua.StatusCode{ua.StatusBadSessionIDInvalid, ua.StatusBadSessionIDInvalid}
		if !reflect.DeepEqual(dr.Results, want) {
			t.Errorf("results = %v, want %v", dr.Results, want)
		}
		if !hasSubscription(svc, subID) {
			t.Errorf("a request without a session deleted subscription %d", subID)
		}
	})
}

// TestDeleteSubscriptionsOwnership pins the access check that was already
// there: a session only deletes its own subscriptions.
func TestDeleteSubscriptionsOwnership(t *testing.T) {
	s := newTestServer(t)
	svc := s.SubscriptionService
	owner, other := newTestSession(s), newTestSession(s)
	subID := createSubscription(t, s, owner, 3600000)

	resp := call(t, s, id.DeleteSubscriptionsRequest_Encoding_DefaultBinary, &ua.DeleteSubscriptionsRequest{
		RequestHeader:   header(other),
		SubscriptionIDs: []uint32{subID},
	})
	if got := resp.(*ua.DeleteSubscriptionsResponse).Results; !reflect.DeepEqual(got, []ua.StatusCode{ua.StatusBadSessionIDInvalid}) {
		t.Fatalf("another session deleting the subscription got %v", got)
	}
	if !hasSubscription(svc, subID) {
		t.Fatalf("another session deleted subscription %d", subID)
	}

	resp = call(t, s, id.DeleteSubscriptionsRequest_Encoding_DefaultBinary, &ua.DeleteSubscriptionsRequest{
		RequestHeader:   header(owner),
		SubscriptionIDs: []uint32{subID, 999999},
	})
	if got := resp.(*ua.DeleteSubscriptionsResponse).Results; !reflect.DeepEqual(got, []ua.StatusCode{ua.StatusOK, ua.StatusBadSubscriptionIDInvalid}) {
		t.Fatalf("the owner deleting its subscription and an unknown one got %v", got)
	}
	waitFor(t, "the subscription to be removed", func() bool { return !hasSubscription(svc, subID) })
}

// startDoomedSubscription registers a subscription whose publishing goroutine
// is bound to panic on its first tick: a publish request is already queued,
// the keepalive count is 0 and the secure channel is missing.
func startDoomedSubscription(s *Server, sess *session) *Subscription {
	svc := s.SubscriptionService

	sub := NewSubscription()
	sub.srv = svc
	sub.Session = sess
	sub.Channel = nil
	sub.ID = 4242
	sub.RevisedPublishingInterval = 1
	sub.RevisedMaxKeepAliveCount = 0
	sub.RevisedLifetimeCount = 100

	sess.PublishRequests <- PubReq{Req: &ua.PublishRequest{RequestHeader: &ua.RequestHeader{}}, ID: 1}

	svc.Mu.Lock()
	svc.Subs[sub.ID] = sub
	sub.running = true
	svc.Mu.Unlock()

	sub.Start()
	return sub
}

func TestSubscriptionRunRecoversFromPanic(t *testing.T) {
	crashProbe(t, "with-logger", func() {
		log := &testLogger{}
		s := newTestServer(t, SetLogger(log))
		svc := s.SubscriptionService

		startDoomedSubscription(s, newTestSession(s))

		// run() removes its own subscription on the way out, so the service
		// must be empty once the panic has been handled.
		waitFor(t, "the doomed subscription to be removed", func() bool { return !hasSubscription(svc, 4242) })

		var reported []string
		for _, line := range log.recorded() {
			if strings.HasPrefix(line, "ERROR: ") && strings.Contains(line, "recovered from panic") {
				reported = append(reported, line)
			}
		}
		if len(reported) != 1 {
			t.Fatalf("expected exactly one error about the recovered panic, got %d:\n%s", len(reported), strings.Join(log.recorded(), "\n"))
		}
		for _, want := range []string{"4242", "goroutine ", "subscription_service.go"} {
			if !strings.Contains(reported[0], want) {
				t.Errorf("the panic report lacks %q:\n%s", want, reported[0])
			}
		}
	})

	crashProbe(t, "without-logger", func() {
		s := newTestServer(t)
		svc := s.SubscriptionService

		startDoomedSubscription(s, newTestSession(s))
		waitFor(t, "the doomed subscription to be removed", func() bool { return !hasSubscription(svc, 4242) })
	})
}
