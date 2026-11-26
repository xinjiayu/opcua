package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P12, over the wire: a real client sends the server a message with more
// nesting than the decoder allows.
//
// The decoder is covered in ua/patch_p12_nesting_test.go. This test checks
// the server's own path for the message: it must neither die nor serve the
// request, and it must go on serving everybody else. The nesting here is
// small, 150 levels. It only has to be over the limit, because the limit is
// what keeps a large one from ever reaching the recursion.
func TestServerSurvivesOverDeepNesting(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)

	// The client of the over-deep request gets a short request timeout: the
	// server will answer neither that request nor the ones that closing the
	// client sends.
	deep, err := opcua.NewClient("opc.tcp://"+addr, opcua.SessionTimeout(time.Minute), opcua.RequestTimeout(time.Second))
	if err != nil {
		t.Fatalf("cannot create client: %v", err)
	}
	t.Cleanup(func() { deep.Close(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := deep.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// a Variant nested 150 levels deep, 50 more than the decoder allows
	value := ua.MustVariant(int32(1))
	for i := 0; i < 150; i++ {
		value = ua.MustVariant(value)
	}
	req := &ua.WriteRequest{NodesToWrite: []*ua.WriteValue{{
		NodeID:      ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime),
		AttributeID: ua.AttributeIDValue,
		Value:       &ua.DataValue{EncodingMask: ua.DataValueValue, Value: value},
	}}}

	answered := make(chan ua.Response, 1)
	go func() {
		// Client.Send adds the token of the session, so the request would
		// reach the Write handler if the decoder let it through.
		_ = deep.Send(context.Background(), req, func(resp ua.Response) error {
			select {
			case answered <- resp:
			default:
			}
			return nil
		})
	}()

	select {
	case resp := <-answered:
		t.Fatalf("the server answered a value nested 150 levels deep with %s", describeResponse(resp))
	case <-time.After(500 * time.Millisecond):
	}

	// and the server is still there for the next client
	next := connectWithTimeout(t, addr, time.Minute)
	readCtx, cancelRead := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelRead()
	read, err := next.Read(readCtx, &ua.ReadRequest{
		TimestampsToReturn: ua.TimestampsToReturnBoth,
		NodesToRead:        []*ua.ReadValueID{{NodeID: ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime), AttributeID: ua.AttributeIDValue, DataEncoding: &ua.QualifiedName{}}},
	})
	if err != nil {
		t.Fatalf("Read from a second client after the over-deep request: %v", err)
	}
	if len(read.Results) != 1 || read.Results[0].Status != ua.StatusOK {
		t.Fatalf("the second client got %v", read.Results)
	}
}
