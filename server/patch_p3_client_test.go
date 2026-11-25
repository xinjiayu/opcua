package server

import (
	"context"
	"testing"
	"time"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// TestClientDecodesLargeRealResponses sends the client library of this module,
// whose decoder carries P3, against real server responses with long arrays.
// The check in decodeSlice rejects a declared element count above the number
// of bytes left in the message. Every element takes at least one byte on the
// wire, so no legitimate response can trip it; this pins that for responses
// of a size a gateway really produces.

func TestClientDecodesLargeRealResponses(t *testing.T) {
	s := newLoopbackServer(t)
	addr := startTestServer(t, s)
	c := connectTestClient(t, addr)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// a read of many entries at once: one entry in NodesToRead per entry. All
	// name the same node, which exists, so every result has to succeed.
	const nodes = 500
	existing := ua.NewNumericNodeID(0, id.Server_ServerStatus_CurrentTime)
	toRead := make([]*ua.ReadValueID, nodes)
	for i := range toRead {
		toRead[i] = &ua.ReadValueID{
			NodeID:       existing,
			AttributeID:  ua.AttributeIDBrowseName,
			DataEncoding: &ua.QualifiedName{},
		}
	}
	read, err := c.Read(ctx, &ua.ReadRequest{TimestampsToReturn: ua.TimestampsToReturnBoth, NodesToRead: toRead})
	if err != nil {
		t.Fatalf("Read of %d entries: %v", nodes, err)
	}
	if len(read.Results) != nodes {
		t.Fatalf("Read of %d entries returned %d results", nodes, len(read.Results))
	}
	answered := 0
	for _, dv := range read.Results {
		if dv.Status == ua.StatusOK && dv.Value != nil {
			answered++
		}
	}
	if answered != nodes {
		t.Fatalf("only %d of %d entries were read", answered, nodes)
	}

	// a browse of a node with many references, in both directions
	browse, err := c.Browse(ctx, &ua.BrowseRequest{
		View:                          &ua.ViewDescription{ViewID: ua.NewTwoByteNodeID(0)},
		RequestedMaxReferencesPerNode: 0,
		NodesToBrowse: []*ua.BrowseDescription{{
			NodeID:          ua.NewNumericNodeID(0, id.References),
			BrowseDirection: ua.BrowseDirectionBoth,
			ReferenceTypeID: ua.NewTwoByteNodeID(0),
			IncludeSubtypes: true,
			ResultMask:      0x3f,
		}},
	})
	if err != nil {
		t.Fatalf("Browse of the References type: %v", err)
	}
	if len(browse.Results) != 1 || browse.Results[0].StatusCode != ua.StatusOK || len(browse.Results[0].References) < 2 {
		t.Fatalf("Browse of the References type returned %v", browse.Results)
	}

	t.Logf("the client decoded a ReadResponse with %d results and a BrowseResponse with %d references", len(read.Results), len(browse.Results[0].References))
}
