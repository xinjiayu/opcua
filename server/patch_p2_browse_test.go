package server

import (
	"fmt"
	"slices"
	"testing"

	"github.com/gopcua/opcua/id"
	"github.com/gopcua/opcua/ua"
)

// P2: browsing a node of the address space with IncludeSubtypes=false.
//
// Upstream v0.8.0 (server/view_service.go suitableRefType) tried to drop
// HasSubtype from the list of accepted reference types with a loop whose
// index never changed while the slice shrank on every pass. Whenever HasSubtype
// was not the first entry the loop deleted the wrong elements and then ran off
// the end of the slice: "slice bounds out of range", process dead. The request
// did not need a session.
//
// In the standard ns0 HasSubtype sits at index 54 of the subtypes of
// References, at index 20 for HierarchicalReferences and at index 19 for
// HasChild, so all three crash upstream. It is never at index 0 there; that
// case is covered with a synthetic namespace further down.

// refType returns the ns=0 node id of a reference type.
func refType(i uint32) *ua.NodeID {
	return ua.NewNumericNodeID(0, i)
}

// browseKeys browses node forward, with the given reference type, on a server
// that is not listening, and returns one "<type>><target>" key per reference
// the server returns.
func browseKeys(t *testing.T, s *Server, node, ref uint32, subtypes bool) []string {
	t.Helper()

	resp := call(t, s, id.BrowseRequest_Encoding_DefaultBinary, &ua.BrowseRequest{
		RequestHeader: &ua.RequestHeader{},
		NodesToBrowse: []*ua.BrowseDescription{{
			NodeID:          refType(node),
			BrowseDirection: ua.BrowseDirectionForward,
			ReferenceTypeID: refType(ref),
			IncludeSubtypes: subtypes,
		}},
	})
	br, ok := resp.(*ua.BrowseResponse)
	if !ok || len(br.Results) != 1 {
		t.Fatalf("expected one BrowseResult, got %T", resp)
	}
	if br.Results[0].StatusCode != ua.StatusOK {
		t.Fatalf("browse returned %v", br.Results[0].StatusCode)
	}
	keys := make([]string, 0, len(br.Results[0].References))
	for _, r := range br.Results[0].References {
		keys = append(keys, fmt.Sprintf("%d>%d", r.ReferenceTypeID.IntID(), r.NodeID.NodeID.IntID()))
	}
	return keys
}

// TestSuitableRefTypeNoCrash feeds the three reference types whose subtype
// list contains HasSubtype, subtypes excluded. These crashed upstream.
func TestSuitableRefTypeNoCrash(t *testing.T) {
	cases := []struct {
		name       string
		ref1, ref2 uint32
		want       bool
	}{
		// HasSubtype is entry 54 of the subtypes of References.
		{"references-organizes", id.References, id.Organizes, true},
		{"references-has-type-definition", id.References, id.HasTypeDefinition, true},
		{"references-has-subtype", id.References, id.HasSubtype, false},
		// entry 20 for HierarchicalReferences.
		{"hierarchical-organizes", id.HierarchicalReferences, id.Organizes, true},
		{"hierarchical-has-type-definition", id.HierarchicalReferences, id.HasTypeDefinition, false},
		{"hierarchical-has-subtype", id.HierarchicalReferences, id.HasSubtype, false},
		// entry 19 for HasChild. In the NodeSet2 shipped with this module
		// Organizes is not a subtype of HasChild, it hangs directly off
		// HierarchicalReferences.
		{"has-child-aggregates", id.HasChild, id.Aggregates, true},
		{"has-child-organizes", id.HasChild, id.Organizes, false},
		{"has-child-has-subtype", id.HasChild, id.HasSubtype, false},
	}

	for _, c := range cases {
		crashProbe(t, c.name, func() {
			s := newTestServer(t)
			got := suitableRefType(s, refType(c.ref1), refType(c.ref2), false)
			if got != c.want {
				t.Fatalf("suitableRefType(%s, %s, subtypes=false) = %v, want %v",
					id.Name(c.ref1), id.Name(c.ref2), got, c.want)
			}
		})
	}
}

// TestSuitableRefTypeUnchanged pins the inputs that never reached the broken
// loop. The expected values were recorded on the unpatched copy.
func TestSuitableRefTypeUnchanged(t *testing.T) {
	s := newTestServer(t)

	cases := []struct {
		name       string
		ref1, ref2 uint32
		subtypes   bool
		want       bool
	}{
		// a leaf type has no subtypes to speak of.
		{"organizes-itself", id.Organizes, id.Organizes, false, true},
		{"organizes-other", id.Organizes, id.HasTypeDefinition, false, false},
		{"organizes-itself-with-subtypes", id.Organizes, id.Organizes, true, true},
		// with subtypes included the branch is not taken at all.
		{"references-organizes-with-subtypes", id.References, id.Organizes, true, true},
		{"references-has-subtype-with-subtypes", id.References, id.HasSubtype, true, true},
		{"hierarchical-organizes-with-subtypes", id.HierarchicalReferences, id.Organizes, true, true},
		// NonHierarchicalReferences has no HasSubtype among its subtypes.
		{"nonhierarchical-has-type-definition", id.NonHierarchicalReferences, id.HasTypeDefinition, false, true},
		{"nonhierarchical-organizes", id.NonHierarchicalReferences, id.Organizes, false, false},
		{"nonhierarchical-has-type-definition-with-subtypes", id.NonHierarchicalReferences, id.HasTypeDefinition, true, true},
		// a null ReferenceTypeId accepts everything.
		{"null-reference-type", 0, id.Organizes, false, true},
		{"null-reference-type-with-subtypes", 0, id.Organizes, true, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := suitableRefType(s, refType(c.ref1), refType(c.ref2), c.subtypes)
			if got != c.want {
				t.Fatalf("suitableRefType(%s, %s, subtypes=%v) = %v, want %v",
					id.Name(c.ref1), id.Name(c.ref2), c.subtypes, got, c.want)
			}
		})
	}
}

// TestBrowseObjectsFolderNoCrash sends the crashing inputs through the Browse
// handler. ObjectsFolder carries no HasSubtype reference, so excluding subtypes
// must yield exactly what including them yields.
func TestBrowseObjectsFolderNoCrash(t *testing.T) {
	for _, ref := range []uint32{id.References, id.HierarchicalReferences, id.HasChild} {
		crashProbe(t, id.Name(ref), func() {
			s := newTestServer(t)
			got := browseKeys(t, s, id.ObjectsFolder, ref, false)
			want := browseKeys(t, s, id.ObjectsFolder, ref, true)
			if !slices.Equal(got, want) {
				t.Fatalf("browse of ObjectsFolder with %s, subtypes excluded, returned %v, want %v",
					id.Name(ref), got, want)
			}
		})
	}
}

// TestBrowseObjectsFolderUnchanged pins the Browse results of the inputs that
// never reached the broken loop. The expected values were recorded on the
// unpatched copy.
func TestBrowseObjectsFolderUnchanged(t *testing.T) {
	s := newTestServer(t)

	all := []string{"40>61", "35>31915", "35>2253", "35>23470"}
	organized := []string{"35>31915", "35>2253", "35>23470"}

	cases := []struct {
		name     string
		ref      uint32
		subtypes bool
		want     []string
	}{
		{"organizes", id.Organizes, false, organized},
		{"references-with-subtypes", id.References, true, all},
		{"nonhierarchical", id.NonHierarchicalReferences, false, []string{"40>61"}},
		{"null-reference-type", 0, false, all},
		{"hierarchical-with-subtypes", id.HierarchicalReferences, true, organized},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := browseKeys(t, s, id.ObjectsFolder, c.ref, c.subtypes)
			if !slices.Equal(got, c.want) {
				t.Fatalf("browse of ObjectsFolder with %s, subtypes=%v, returned %v, want %v",
					id.Name(c.ref), c.subtypes, got, c.want)
			}
		})
	}
}

// TestSuitableRefTypeExcludesHasSubtypeListedFirst covers the branch the
// broken loop skipped: a reference type whose first subtype is HasSubtype.
// Upstream left HasSubtype in the accepted list there, so HasSubtype references
// were returned although the client excluded subtypes. No standard ns0
// reference type has that shape, hence the synthetic namespace.
func TestSuitableRefTypeExcludesHasSubtypeListedFirst(t *testing.T) {
	s := newTestServer(t)
	ns := NewNodeNameSpace(s, "urn:gopcua:patch-test:reference-types")
	root := ua.NewNumericNodeID(ns.ID(), 5000)
	// HasSubtype first, then a leaf type so that the subtype list has exactly
	// these two entries.
	ns.AddNode(NewNode(root, nil, []*ua.ReferenceDescription{
		{ReferenceTypeID: hasSubtype, IsForward: true, NodeID: ua.NewNumericExpandedNodeID(0, id.HasSubtype)},
		{ReferenceTypeID: hasSubtype, IsForward: true, NodeID: ua.NewNumericExpandedNodeID(0, id.HasTypeDefinition)},
	}, nil))

	if subs := getSubRefs(s, root); len(subs) != 2 || !subs[0].Equal(hasSubtype) {
		t.Fatalf("test setup: expected HasSubtype to be the first subtype of %s, got %v", root, subs)
	}

	if suitableRefType(s, root, refType(id.HasSubtype), false) {
		t.Fatalf("HasSubtype reference accepted although subtypes are excluded")
	}
	if !suitableRefType(s, root, refType(id.HasTypeDefinition), false) {
		t.Fatalf("HasTypeDefinition reference rejected although only HasSubtype is dropped")
	}
	if !suitableRefType(s, root, refType(id.HasSubtype), true) {
		t.Fatalf("HasSubtype reference rejected although subtypes are included")
	}
}
