package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P9: subscription numbers.
//
// Upstream v0.8.0 (server/subscription_service.go CreateSubscription) handed
// out "number of subscriptions held + 1". After one subscription was deleted
// the next one got the number of another, live, subscription and replaced it
// in the table: the live subscription lost its number, its monitored items
// could not be reached anymore, and its publishing goroutine kept running.
// The client library refuses the duplicate with BadSubscriptionIdInvalid.

func TestSubscriptionIDsAreNotReused(t *testing.T) {
	s := newTestServer(t)
	svc := s.SubscriptionService
	sess := newTestSession(s)

	first := createSubscription(t, s, sess, 3600000)
	second := createSubscription(t, s, sess, 3600000)

	svc.Mu.Lock()
	live := svc.Subs[second]
	svc.Mu.Unlock()
	if first == second || live == nil {
		t.Fatalf("subscriptions got the numbers %d and %d", first, second)
	}

	// the first subscription goes away
	resp := call(t, s, id.DeleteSubscriptionsRequest_Encoding_DefaultBinary, &ua.DeleteSubscriptionsRequest{
		RequestHeader:   header(sess),
		SubscriptionIDs: []uint32{first},
	})
	if got := resp.(*ua.DeleteSubscriptionsResponse).Results; !reflect.DeepEqual(got, []ua.StatusCode{ua.StatusOK}) {
		t.Fatalf("deleting subscription %d got %v", first, got)
	}
	waitFor(t, "the first subscription to be removed", func() bool { return !hasSubscription(svc, first) })

	// a new subscription gets a number of its own
	third := createSubscription(t, s, sess, 3600000)
	if third == second {
		t.Fatalf("the new subscription got %d, the number of the live second subscription", third)
	}

	// and the second subscription is still the one behind its number
	svc.Mu.Lock()
	behind, count := svc.Subs[second], len(svc.Subs)
	svc.Mu.Unlock()
	if behind != live {
		t.Fatalf("subscription %d is no longer the second subscription", second)
	}
	if count != 2 {
		t.Fatalf("%d subscriptions are registered, want the second and the new one", count)
	}
}

// expectNotification reads notifications until one arrives for the given
// subscription that is not an error.
func expectNotification(t *testing.T, what string, notifications chan *opcua.PublishNotificationData, sub *opcua.Subscription) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no notification for subscription %d arrived while waiting for %s", sub.SubscriptionID, what)
		}
		select {
		case n := <-notifications:
			if n.Error != nil {
				t.Fatalf("subscription %d reported %v while waiting for %s", sub.SubscriptionID, n.Error, what)
			}
			if n.SubscriptionID == sub.SubscriptionID {
				return
			}
		case <-time.After(time.Until(deadline)):
		}
	}
}

// dropNotifications discards what is queued for a subscription.
func dropNotifications(notifications chan *opcua.PublishNotificationData) {
	for {
		select {
		case <-notifications:
		default:
			return
		}
	}
}

// TestSubscriptionIDsAreNotReusedOverTheWire goes through a real client with
// real publishing: cancelling one subscription and opening another must leave
// the other subscription delivering.
func TestSubscriptionIDsAreNotReusedOverTheWire(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectTestClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	node := ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime)
	item := &ua.MonitoredItemCreateRequest{
		ItemToMonitor:       &ua.ReadValueID{NodeID: node, AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}},
		MonitoringMode:      ua.MonitoringModeReporting,
		RequestedParameters: &ua.MonitoringParameters{ClientHandle: 1, SamplingInterval: -1, QueueSize: 10, DiscardOldest: true},
	}

	// subscribe monitors the node in a new subscription and returns the
	// subscription together with the channel its notifications arrive on
	subscribe := func() (*opcua.Subscription, chan *opcua.PublishNotificationData) {
		t.Helper()

		notifications := make(chan *opcua.PublishNotificationData, 16)
		sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{Interval: 100 * time.Millisecond}, notifications)
		if err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		if _, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, item); err != nil {
			t.Fatalf("Monitor in subscription %d: %v", sub.SubscriptionID, err)
		}
		return sub, notifications
	}

	first, _ := subscribe()
	second, secondNotifications := subscribe()

	// both deliver
	s.ChangeNotification(node)
	expectNotification(t, "the second subscription to deliver", secondNotifications, second)

	// the first subscription goes away
	if err := first.Cancel(ctx); err != nil {
		t.Fatalf("Cancel subscription %d: %v", first.SubscriptionID, err)
	}
	waitFor(t, "the first subscription to be removed", func() bool {
		return !hasSubscription(s.SubscriptionService, first.SubscriptionID)
	})

	// A new subscription gets a number of its own. Upstream handed out the
	// number of the live second subscription here, which the client refuses.
	third, thirdNotifications := subscribe()
	if third.SubscriptionID == second.SubscriptionID {
		t.Fatalf("the new subscription got %d, the number of the live second subscription", third.SubscriptionID)
	}

	// and the second subscription still delivers
	dropNotifications(secondNotifications)
	s.ChangeNotification(node)
	expectNotification(t, "the second subscription to still deliver", secondNotifications, second)
	expectNotification(t, "the new subscription to deliver", thirdNotifications, third)
}
