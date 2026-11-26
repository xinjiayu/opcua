package server

import (
	"reflect"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P1: a Write aimed at a namespace the server has not registered.
//
// Upstream v0.8.0 stored BadNodeNotInView for the result but then went on to
// call SetAttribute on the nil namespace, which panicked and took the whole
// process down. One WriteRequest naming a node in ns=99 was enough and the
// request did not even need a session.
//
// The mixed cases make sure the added `continue` keeps the results aligned
// with the request: the entries after the bad one are still processed.
func TestWriteUnregisteredNamespace(t *testing.T) {
	value := func() *ua.DataValue {
		return &ua.DataValue{EncodingMask: ua.DataValueValue, Value: ua.MustVariant(int32(1))}
	}
	unregistered := &ua.WriteValue{NodeID: ua.NewStringNodeID(99, "x"), AttributeID: ua.AttributeIDValue, Value: value()}
	// a node in ns0 that does not exist; the namespace is valid, so the
	// regular "unknown node" status is expected for it.
	unknown := &ua.WriteValue{NodeID: ua.NewNumericNodeID(0, 0x7fffffff), AttributeID: ua.AttributeIDValue, Value: value()}

	cases := []struct {
		name  string
		nodes []*ua.WriteValue
		want  []ua.StatusCode
	}{
		{"unregistered-only", []*ua.WriteValue{unregistered}, []ua.StatusCode{ua.StatusBadNodeIDUnknown}},
		{"unregistered-then-unknown", []*ua.WriteValue{unregistered, unknown}, []ua.StatusCode{ua.StatusBadNodeIDUnknown, ua.StatusBadNodeIDUnknown}},
		{"unknown-then-unregistered", []*ua.WriteValue{unknown, unregistered}, []ua.StatusCode{ua.StatusBadNodeIDUnknown, ua.StatusBadNodeIDUnknown}},
	}

	for _, c := range cases {
		crashProbe(t, c.name, func() {
			s := newTestServer(t)
			resp := call(t, s, id.WriteRequest_Encoding_DefaultBinary, &ua.WriteRequest{
				RequestHeader: &ua.RequestHeader{},
				NodesToWrite:  c.nodes,
			})
			wr, ok := resp.(*ua.WriteResponse)
			if !ok {
				t.Fatalf("expected *ua.WriteResponse, got %T", resp)
			}
			if !reflect.DeepEqual(wr.Results, c.want) {
				t.Fatalf("results = %v, want %v", wr.Results, c.want)
			}
		})
	}
}
