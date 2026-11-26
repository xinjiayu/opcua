package server

import (
	"reflect"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P5: the monitored item service.
//
// (a) DeleteMonitoredItems and SetMonitoringMode dereferenced the item before
//     checking that it exists. A single unknown monitored item id in the
//     request panicked on the one goroutine that serves requests: process
//     dead.
// (b) Both handlers wrote BadSessionIDInvalid for an item that belongs to
//     another session and then overwrote it with StatusOK. Any client could
//     delete or reconfigure the monitored items of any other client and was
//     told that it worked.

// unknownItem is a monitored item id that was never handed out.
const unknownItem = 999999

// monitoredItemFixture holds a server with two sessions, a subscription that
// belongs to the first one and a monitored item inside that subscription.
type monitoredItemFixture struct {
	s     *Server
	svc   *MonitoredItemService
	owner *session
	other *session
	subID uint32
	item  uint32
}

func newMonitoredItemFixture(t *testing.T) *monitoredItemFixture {
	t.Helper()

	s := newTestServer(t)
	f := &monitoredItemFixture{
		s:     s,
		svc:   s.MonitoredItemService,
		owner: newTestSession(s),
		other: newTestSession(s),
		subID: 7,
		item:  11,
	}

	// The subscription is registered without its publishing goroutine; the
	// handlers under test only look at the bookkeeping structures.
	sub := NewSubscription()
	sub.srv = s.SubscriptionService
	sub.Session = f.owner
	sub.ID = f.subID
	sub.RevisedPublishingInterval = 3600000
	s.SubscriptionService.Mu.Lock()
	s.SubscriptionService.Subs[sub.ID] = sub
	s.SubscriptionService.Mu.Unlock()

	// what CreateMonitoredItems stores for a monitored item
	mi := &MonitoredItem{
		ID:  f.item,
		Sub: sub,
		Req: &ua.MonitoredItemCreateRequest{
			ItemToMonitor:       &ua.ReadValueID{NodeID: ua.NewNumericNodeID(0, 2258), AttributeID: ua.AttributeIDValue},
			RequestedParameters: &ua.MonitoringParameters{ClientHandle: 1},
		},
		Mode: ua.MonitoringModeReporting,
	}
	f.svc.Mu.Lock()
	f.svc.Items[mi.ID] = mi
	f.svc.Nodes[mi.Req.ItemToMonitor.NodeID.String()] = []*MonitoredItem{mi}
	f.svc.Subs[sub.ID] = []*MonitoredItem{mi}
	f.svc.Mu.Unlock()

	return f
}

// monitoredItem returns the item with the given id, or nil.
func (f *monitoredItemFixture) monitoredItem(id uint32) *MonitoredItem {
	f.svc.Mu.Lock()
	defer f.svc.Mu.Unlock()

	return f.svc.Items[id]
}

// checkItemResults calls one of the two handlers and compares the per-item
// results with want.
func checkItemResults(t *testing.T, f *monitoredItemFixture, typeID uint16, sess *session, ids []uint32, want []ua.StatusCode) {
	t.Helper()

	var req ua.Request
	switch typeID {
	case id.DeleteMonitoredItemsRequest_Encoding_DefaultBinary:
		req = &ua.DeleteMonitoredItemsRequest{RequestHeader: header(sess), SubscriptionID: f.subID, MonitoredItemIDs: ids}
	case id.SetMonitoringModeRequest_Encoding_DefaultBinary:
		req = &ua.SetMonitoringModeRequest{RequestHeader: header(sess), SubscriptionID: f.subID, MonitoringMode: ua.MonitoringModeDisabled, MonitoredItemIDs: ids}
	default:
		t.Fatalf("unexpected service %d", typeID)
	}

	var got []ua.StatusCode
	switch resp := call(t, f.s, typeID, req).(type) {
	case *ua.DeleteMonitoredItemsResponse:
		got = resp.Results
	case *ua.SetMonitoringModeResponse:
		got = resp.Results
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results for %v = %v, want %v", ids, got, want)
	}
}

func TestDeleteMonitoredItemsRejectsUnknownID(t *testing.T) {
	known, unknown := []uint32{11}, []uint32{unknownItem}

	cases := []struct {
		name string
		ids  []uint32
		want []ua.StatusCode
	}{
		{"after-known-item", append(append([]uint32{}, known...), unknown...), []ua.StatusCode{ua.StatusOK, ua.StatusBadMonitoredItemIDInvalid}},
		{"before-known-item", append(append([]uint32{}, unknown...), known...), []ua.StatusCode{ua.StatusBadMonitoredItemIDInvalid, ua.StatusOK}},
		{"alone", unknown, []ua.StatusCode{ua.StatusBadMonitoredItemIDInvalid}},
	}

	for _, c := range cases {
		crashProbe(t, c.name, func() {
			f := newMonitoredItemFixture(t)
			checkItemResults(t, f, id.DeleteMonitoredItemsRequest_Encoding_DefaultBinary, f.owner, c.ids, c.want)
		})
	}
}

func TestSetMonitoringModeRejectsUnknownID(t *testing.T) {
	known, unknown := []uint32{11}, []uint32{unknownItem}

	cases := []struct {
		name string
		ids  []uint32
		want []ua.StatusCode
	}{
		{"after-known-item", append(append([]uint32{}, known...), unknown...), []ua.StatusCode{ua.StatusOK, ua.StatusBadMonitoredItemIDInvalid}},
		{"before-known-item", append(append([]uint32{}, unknown...), known...), []ua.StatusCode{ua.StatusBadMonitoredItemIDInvalid, ua.StatusOK}},
		{"alone", unknown, []ua.StatusCode{ua.StatusBadMonitoredItemIDInvalid}},
	}

	for _, c := range cases {
		crashProbe(t, c.name, func() {
			f := newMonitoredItemFixture(t)
			checkItemResults(t, f, id.SetMonitoringModeRequest_Encoding_DefaultBinary, f.owner, c.ids, c.want)
		})
	}
}

func TestDeleteMonitoredItemsRejectsOtherSession(t *testing.T) {
	f := newMonitoredItemFixture(t)

	checkItemResults(t, f, id.DeleteMonitoredItemsRequest_Encoding_DefaultBinary, f.other, []uint32{f.item, unknownItem},
		[]ua.StatusCode{ua.StatusBadSessionIDInvalid, ua.StatusBadMonitoredItemIDInvalid})

	// upstream removed the item in the background, hence the grace period.
	time.Sleep(50 * time.Millisecond)
	if item := f.monitoredItem(f.item); item == nil {
		t.Fatalf("another session deleted monitored item %d", f.item)
	}
}

func TestSetMonitoringModeRejectsOtherSession(t *testing.T) {
	f := newMonitoredItemFixture(t)

	checkItemResults(t, f, id.SetMonitoringModeRequest_Encoding_DefaultBinary, f.other, []uint32{f.item, unknownItem},
		[]ua.StatusCode{ua.StatusBadSessionIDInvalid, ua.StatusBadMonitoredItemIDInvalid})

	if item := f.monitoredItem(f.item); item == nil {
		t.Fatalf("monitored item %d vanished", f.item)
	} else if item.Mode != ua.MonitoringModeReporting {
		t.Fatalf("another session changed monitored item %d to %v", f.item, item.Mode)
	}
}

// TestMonitoredItemHandlersServeOwner pins the owner's own requests: they
// succeed, and only the owner's items are touched.
func TestMonitoredItemHandlersServeOwner(t *testing.T) {
	f := newMonitoredItemFixture(t)

	checkItemResults(t, f, id.SetMonitoringModeRequest_Encoding_DefaultBinary, f.owner, []uint32{f.item, unknownItem},
		[]ua.StatusCode{ua.StatusOK, ua.StatusBadMonitoredItemIDInvalid})
	if item := f.monitoredItem(f.item); item == nil || item.Mode != ua.MonitoringModeDisabled {
		t.Fatalf("the owner could not change the monitoring mode of item %d", f.item)
	}

	checkItemResults(t, f, id.DeleteMonitoredItemsRequest_Encoding_DefaultBinary, f.owner, []uint32{f.item},
		[]ua.StatusCode{ua.StatusOK})
	waitFor(t, "the item to be removed", func() bool { return f.monitoredItem(f.item) == nil })
}
