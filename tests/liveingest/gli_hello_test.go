package liveingest_test

// GLI-023, GLI-025, GLI-030..037, GLI-053 (streams) and GLI-124 (b): the hello
// state machine, session allocation, fencing, overflow, idle and leadership.

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/aetertinas9/data-path-assurance/internal/app/liveingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
)

// gliwUnknownField is a well-formed unknown field (number 999, varint 1).
func gliwUnknownField() []byte {
	return protowire.AppendVarint(protowire.AppendTag(nil, 999, protowire.VarintType), 1)
}

func gliwHelloMsg(h *ingestpb.ClientHello) *ingestpb.ClientFrame {
	return &ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: h}}
}

// gliwSendFrame sends an arbitrary ClientFrame and returns the first server answer.
func gliwSendFrame(st ingestpb.Ingest_StreamClient, msg *ingestpb.ClientFrame) (*ingestpb.ServerHello, error) {
	if err := st.Send(msg); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	fr, err := st.Recv()
	if err != nil {
		return nil, err
	}
	if fr.GetHello() == nil {
		return nil, errors.New("gliw: answer is not a ServerHello")
	}
	return fr.GetHello(), nil
}

// gliwDrainHelloBucket makes count successful hellos for the node at the current
// fake time (each fences the previous stream and uses one hello token).
func gliwDrainHelloBucket(t *testing.T, s *gliServer, n gliwNode, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		_, closeFn, _ := s.Connect(t, n)
		closeFn()
	}
}

func TestGLI030_HelloSuccessAndProfile(t *testing.T) {
	for _, profile := range []string{"live:default", "live:custom-profile.1"} {
		profile := profile
		t.Run(profile, func(t *testing.T) {
			s := gliStartServer(t, func(c *ingest.Config) { c.CollectorProfileID = profile })
			n := s.AddNode(t, "ok")
			_, _, h := s.Connect(t, n)
			if h.GetSession() != 1 {
				t.Errorf("first session = %d, want 1", h.GetSession())
			}
			if h.GetCollectorProfileId() != profile {
				t.Errorf("collector_profile_id = %q, want %q", h.GetCollectorProfileId(), profile)
			}
			calls := s.Sessions.CallsFor(n.UID)
			if len(calls) != 1 || calls[0].After != 0 || calls[0].Result != 1 || calls[0].Err != nil {
				t.Errorf("session store calls = %+v, want one call {After:0 Result:1}", calls)
			}
			s.WantNoObservation(t, "hello alone gives no observation", n)
		})
	}
}

// TestGLI030_HelloFieldFormat: every malformed hello is InvalidArgument at step
// (4) — before binding (5), before the hello bucket (6), the node directory (7)
// and the store (8) — so none of them consumes a hello token.
func TestGLI030_HelloFieldFormat(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "fmt")
	const marker = "GLIWMARK"
	rep := func(c string, k int) string { return strings.Repeat(c, k) }
	cases := []struct {
		name     string
		mut      func(h *ingestpb.ClientHello)
		unkHello bool
		unkFrame bool
	}{
		{name: "version empty", mut: func(h *ingestpb.ClientHello) { h.Version = "" }},
		{name: "version v1", mut: func(h *ingestpb.ClientHello) { h.Version = "v1" }},
		{name: "version upper case", mut: func(h *ingestpb.ClientHello) { h.Version = "V1alpha1" }},
		{name: "version trailing space", mut: func(h *ingestpb.ClientHello) { h.Version = "v1alpha1 " }},
		{name: "version leading space", mut: func(h *ingestpb.ClientHello) { h.Version = " v1alpha1" }},
		{name: "version trailing newline", mut: func(h *ingestpb.ClientHello) { h.Version = "v1alpha1\n" }},
		{name: "version v1alpha2", mut: func(h *ingestpb.ClientHello) { h.Version = "v1alpha2" }},
		{name: "version carries marker", mut: func(h *ingestpb.ClientHello) { h.Version = marker + "-version" }},
		{name: "cluster_id empty", mut: func(h *ingestpb.ClientHello) { h.ClusterId = "" }},
		{name: "cluster_id with space", mut: func(h *ingestpb.ClientHello) { h.ClusterId = "lab a" }},
		{name: "cluster_id with plus", mut: func(h *ingestpb.ClientHello) { h.ClusterId = "lab+a" }},
		{name: "cluster_id 129 bytes", mut: func(h *ingestpb.ClientHello) { h.ClusterId = rep("a", 129) }},
		{name: "cluster_id with newline", mut: func(h *ingestpb.ClientHello) { h.ClusterId = "lab-a\n" }},
		{name: "node_name empty", mut: func(h *ingestpb.ClientHello) { h.NodeName = "" }},
		{name: "node_name 254 bytes", mut: func(h *ingestpb.ClientHello) { h.NodeName = rep("n", 254) }},
		{name: "node_name with space (marker)", mut: func(h *ingestpb.ClientHello) { h.NodeName = marker + "-name with space" }},
		{name: "node_name with DEL", mut: func(h *ingestpb.ClientHello) { h.NodeName = "n\x7f" }},
		{name: "node_name with control byte", mut: func(h *ingestpb.ClientHello) { h.NodeName = "n\x01" }},
		{name: "node_name non-ASCII UTF-8", mut: func(h *ingestpb.ClientHello) { h.NodeName = "né" }},
		{name: "node_uid empty", mut: func(h *ingestpb.ClientHello) { h.NodeUid = "" }},
		{name: "node_uid 129 bytes", mut: func(h *ingestpb.ClientHello) { h.NodeUid = rep("u", 129) }},
		{name: "node_uid with space (marker)", mut: func(h *ingestpb.ClientHello) { h.NodeUid = marker + "-uid with space" }},
		{name: "node_uid with control byte", mut: func(h *ingestpb.ClientHello) { h.NodeUid = "u\x01" }},
		{name: "node_uid non-ASCII UTF-8", mut: func(h *ingestpb.ClientHello) { h.NodeUid = "ué" }},
		{name: "boot_id empty", mut: func(h *ingestpb.ClientHello) { h.BootId = "" }},
		{name: "boot_id 129 bytes", mut: func(h *ingestpb.ClientHello) { h.BootId = rep("b", 129) }},
		{name: "boot_id with space", mut: func(h *ingestpb.ClientHello) { h.BootId = "boot id" }},
		{name: "boot_id with DEL", mut: func(h *ingestpb.ClientHello) { h.BootId = "b\x7f" }},
		{name: "boot_id non-ASCII UTF-8 (marker)", mut: func(h *ingestpb.ClientHello) { h.BootId = marker + "-booté" }},
		{name: "unknown field in ClientHello", mut: func(h *ingestpb.ClientHello) {}, unkHello: true},
		{name: "unknown field in ClientFrame", mut: func(h *ingestpb.ClientHello) {}, unkFrame: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, closeFn := s.Stream(t, n.Cert)
			defer closeFn()
			h := s.HelloMsg(n)
			tc.mut(h)
			msg := gliwHelloMsg(h)
			if tc.unkHello {
				h.ProtoReflect().SetUnknown(gliwUnknownField())
			}
			if tc.unkFrame {
				msg.ProtoReflect().SetUnknown(gliwUnknownField())
			}
			_, err := gliwSendFrame(st, msg)
			gliwWantCode(t, "hello", err, codes.InvalidArgument, marker)
		})
	}
	if c := s.Sessions.Calls(); len(c) != 0 {
		t.Fatalf("malformed hellos reached the session store: %+v", c)
	}
	if c := s.Nodes.Calls(); c != 0 {
		t.Fatalf("malformed hellos reached the node directory %d times", c)
	}
	// None of the rejected hellos used a token: the full burst of 3 still passes
	// and only the 4th is rate limited.
	for i := 0; i < 3; i++ {
		_, closeFn, _ := s.Connect(t, n)
		closeFn()
	}
	st, closeFn := s.Stream(t, n.Cert)
	defer closeFn()
	_, err := gliwHello(st, s.HelloMsg(n))
	gliwWantCode(t, "fourth hello at the same instant", err, codes.ResourceExhausted)
	if logs := s.LogText(); strings.Contains(logs, marker) {
		t.Errorf("the server log contains the marker string taken from a hello")
	}
}

// TestGLI030_ValidBoundaryValues: values exactly at the limits are accepted.
func TestGLI030_ValidBoundaryValues(t *testing.T) {
	s := gliStartServer(t)
	cases := []struct {
		name     string
		nodeName string
		bootID   string
	}{
		{"node_name 253 bytes", strings.Repeat("n", 253), "boot-a"},
		{"node_name 1 byte", "n", "boot-b"},
		{"boot_id 128 bytes", "gliw-boot128", strings.Repeat("b", 128)},
		{"boot_id 1 byte", "gliw-boot1", "b"},
		{"boot_id with every printable punctuation", "gliw-boot-punct", "!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~"},
		{"node_name with dots and colons", "gliw.node:1_x", "boot-c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := s.AddNode(t, "bv")
			n.Name, n.Boot = tc.nodeName, tc.bootID
			s.Nodes.Put(n.Name, n.UID)
			_, _, h := s.Connect(t, n)
			if h.GetSession() <= 0 {
				t.Fatalf("session %d", h.GetSession())
			}
		})
	}
}

func TestGLI030_FirstAndLaterMessages(t *testing.T) {
	s := gliStartServer(t)
	t.Run("snapshot frame instead of hello", func(t *testing.T) {
		n := s.AddNode(t, "first")
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		if err := gliwSend(st, gliwFrame(t, n, 1, 0)); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "frame as first message", st, codes.InvalidArgument)
		if c := s.Sessions.CallsFor(n.UID); len(c) != 0 {
			t.Errorf("session store called: %+v", c)
		}
	})
	t.Run("empty ClientFrame instead of hello", func(t *testing.T) {
		n := s.AddNode(t, "empty")
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		if err := st.Send(&ingestpb.ClientFrame{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "empty first message", st, codes.InvalidArgument)
	})
	t.Run("second hello", func(t *testing.T) {
		n := s.AddNode(t, "second")
		st, _, _ := s.Connect(t, n)
		if err := st.Send(gliwHelloMsg(s.HelloMsg(n))); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "second hello", st, codes.InvalidArgument)
		if c := s.Sessions.CallsFor(n.UID); len(c) != 1 {
			t.Errorf("the second hello allocated another session: %+v", c)
		}
	})
	t.Run("empty ClientFrame after hello", func(t *testing.T) {
		n := s.AddNode(t, "emptylater")
		st, _, _ := s.Connect(t, n)
		if err := st.Send(&ingestpb.ClientFrame{}); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "empty message after hello", st, codes.InvalidArgument)
	})
	t.Run("unknown field in a frame wrapper after hello", func(t *testing.T) {
		n := s.AddNode(t, "wrapunk")
		st, _, h := s.Connect(t, n)
		msg := gliwSnapshotMsg(gliwFrame(t, n, h.GetSession(), 0))
		msg.ProtoReflect().SetUnknown(gliwUnknownField())
		if err := st.Send(msg); err != nil {
			t.Fatalf("send: %v", err)
		}
		ack, err := gliwRecvAck(st)
		if err != nil {
			t.Fatalf("no ack: %v", err)
		}
		if ack.GetCode() != ingestpb.AckCode_INVALID {
			t.Fatalf("ack %v, want INVALID for an unknown field in the ClientFrame envelope", ack.GetCode())
		}
		gliwExpectEnd(t, "after INVALID", st, codes.InvalidArgument)
		s.WantNoObservation(t, "rejected frame", n)
	})
}

func TestGLI030_HelloTimeout(t *testing.T) {
	s := gliStartServer(t, gliwLimits(ingest.Limits{HelloTimeout: time.Second}))
	t.Run("no hello at all", func(t *testing.T) {
		n := s.AddNode(t, "silent")
		st, _ := s.Stream(t, n.Cert)
		start := time.Now()
		_, err := st.Recv()
		took := time.Since(start)
		gliwWantCode(t, "silent stream", err, codes.DeadlineExceeded)
		if took < 800*time.Millisecond || took > 4500*time.Millisecond {
			t.Errorf("the stream ended after %v, want about HelloTimeout (1 s, +2 s load margin)", took)
		}
		if c := s.Sessions.CallsFor(n.UID); len(c) != 0 {
			t.Errorf("session store called: %+v", c)
		}
	})
	t.Run("hello inside the window", func(t *testing.T) {
		n := s.AddNode(t, "inwindow")
		st, _ := s.Stream(t, n.Cert)
		time.Sleep(300 * time.Millisecond)
		if _, err := gliwHello(st, s.HelloMsg(n)); err != nil {
			t.Fatalf("hello at 0.3 s was rejected: %v", err)
		}
	})
	t.Run("hello after the window", func(t *testing.T) {
		n := s.AddNode(t, "late")
		st, _ := s.Stream(t, n.Cert)
		time.Sleep(1700 * time.Millisecond)
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "late hello", err, codes.DeadlineExceeded)
		if c := s.Sessions.CallsFor(n.UID); len(c) != 0 {
			t.Errorf("a late hello reached the session store: %+v", c)
		}
	})
}

// TestGLI030_StepOrder proves the order of the hello steps (4) < (5) < (6) < (7)
// < (8) and (2) < (3) by combining two violations in one hello.
func TestGLI030_StepOrder(t *testing.T) {
	t.Run("(2) before (3): not leader answers without a hello", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "o2")
		s.Leader.Set(false)
		st, _ := s.Stream(t, n.Cert)
		start := time.Now()
		_, err := st.Recv() // nothing was sent
		gliwWantCode(t, "non-leader stream without hello", err, codes.Unavailable)
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("answer after %v: the stream waited for the hello timeout instead of refusing at once", d)
		}
		if len(s.Sessions.Calls()) != 0 || s.Nodes.Calls() != 0 {
			t.Errorf("a non-leader touched the store (%d) or the directory (%d)", len(s.Sessions.Calls()), s.Nodes.Calls())
		}
	})
	t.Run("(4) before (5)", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "o4")
		st, _ := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.Version, h.ClusterId, h.NodeUid = "v2", "lab-other", "other-uid"
		_, err := gliwHello(st, h)
		gliwWantCode(t, "bad version and mismatching binding", err, codes.InvalidArgument)
	})
	t.Run("(4) before (5): 129-byte uid beats the uid mismatch", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "o4b")
		st, _ := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.NodeUid = strings.Repeat("u", 129)
		_, err := gliwHello(st, h)
		gliwWantCode(t, "129-byte node_uid", err, codes.InvalidArgument)
	})
	t.Run("(5) accepts any printable uid and then fails on the binding", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "o5")
		st, _ := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.NodeUid = "uid/with/slashes"
		_, err := gliwHello(st, h)
		gliwWantCode(t, "printable uid outside the SAN set", err, codes.PermissionDenied)
	})
	t.Run("(5) before (6): binding failure beats an empty bucket", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "o5b")
		gliwDrainHelloBucket(t, s, n, 3)
		st, _ := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.ClusterId = "lab-b"
		_, err := gliwHello(st, h)
		gliwWantCode(t, "cluster mismatch with an empty bucket", err, codes.PermissionDenied)
		// ... and the real hello is what the bucket refuses.
		st2, _ := s.Stream(t, n.Cert)
		_, err = gliwHello(st2, s.HelloMsg(n))
		gliwWantCode(t, "valid hello with an empty bucket", err, codes.ResourceExhausted)
	})
	t.Run("(6) before (7): empty bucket beats an unknown node", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "o6")
		gliwDrainHelloBucket(t, s, n, 3)
		nodes := s.Nodes.Calls()
		st, _ := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.NodeName = "gliw-ghost-node"
		_, err := gliwHello(st, h)
		gliwWantCode(t, "unknown node with an empty bucket", err, codes.ResourceExhausted)
		if s.Nodes.Calls() != nodes {
			t.Errorf("the node directory was asked although the hello bucket was empty")
		}
	})
	t.Run("(7) before (8): unknown node never reaches the store", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "o7")
		st, _ := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.NodeName = "gliw-ghost-node"
		_, err := gliwHello(st, h)
		gliwWantCode(t, "unknown node", err, codes.PermissionDenied)
		if c := s.Sessions.Calls(); len(c) != 0 {
			t.Errorf("the session store was called for an unknown node: %+v", c)
		}
		if s.Nodes.Calls() != 1 {
			t.Errorf("node directory calls = %d, want 1", s.Nodes.Calls())
		}
	})
}

func TestGLI023_ScopeBinding(t *testing.T) {
	s := gliStartServer(t)
	other := s.AddNode(t, "other")
	cases := []struct {
		name      string
		mut       func(h *ingestpb.ClientHello, n gliwNode)
		wantAsked bool // the node directory is asked (step 7 reached)
		prep      func(n gliwNode)
	}{
		{name: "hello cluster differs from certificate and configuration",
			mut: func(h *ingestpb.ClientHello, n gliwNode) { h.ClusterId = "lab-b" }},
		{name: "hello cluster differs only by case",
			mut: func(h *ingestpb.ClientHello, n gliwNode) { h.ClusterId = "Lab-A" }},
		{name: "hello node_uid differs from the certificate",
			mut: func(h *ingestpb.ClientHello, n gliwNode) { h.NodeUid = other.UID }},
		{name: "hello node_uid differs only by case",
			mut: func(h *ingestpb.ClientHello, n gliwNode) { h.NodeUid = strings.ToUpper(n.UID) }},
		{name: "another node's name and uid with this certificate",
			mut: func(h *ingestpb.ClientHello, n gliwNode) { h.NodeName, h.NodeUid = other.Name, other.UID }},
		{name: "another node's name with this certificate's uid (directory says other uid)",
			mut:       func(h *ingestpb.ClientHello, n gliwNode) { h.NodeName = other.Name },
			wantAsked: true},
		{name: "directory reports a different uid for the node",
			mut:       func(h *ingestpb.ClientHello, n gliwNode) {},
			prep:      func(n gliwNode) { s.Nodes.Put(n.Name, other.UID) },
			wantAsked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := s.AddNode(t, "bind")
			if tc.prep != nil {
				tc.prep(n)
			}
			asked := s.Nodes.Calls()
			st, closeFn := s.Stream(t, n.Cert)
			defer closeFn()
			h := s.HelloMsg(n)
			tc.mut(h, n)
			_, err := gliwHello(st, h)
			gliwWantCode(t, "hello", err, codes.PermissionDenied, n.UID, other.UID, n.Name, other.Name)
			if got := s.Nodes.Calls() - asked; tc.wantAsked && got != 1 || !tc.wantAsked && got != 0 {
				t.Errorf("node directory calls = %d (wantAsked=%v)", got, tc.wantAsked)
			}
			if c := s.Sessions.CallsFor(n.UID); len(c) != 0 {
				t.Errorf("session store called: %+v", c)
			}
		})
	}
}

// TestGLI023_CertificateOfAnotherClusterOrServerConfig: the third leg of the
// binding is the configured cluster.
func TestGLI023_CertificateOfAnotherClusterOrServerConfig(t *testing.T) {
	t.Run("certificate cluster differs from the configured cluster", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "xc")
		n.Cert = s.PKI.NodeCert(t, "lab-b", n.UID)
		for _, helloCluster := range []string{"lab-a", "lab-b"} {
			st, closeFn := s.Stream(t, n.Cert)
			h := s.HelloMsg(n)
			h.ClusterId = helloCluster
			_, err := gliwHello(st, h)
			gliwWantCode(t, "hello cluster "+helloCluster, err, codes.PermissionDenied)
			closeFn()
		}
		if c := s.Sessions.Calls(); len(c) != 0 {
			t.Errorf("session store called: %+v", c)
		}
	})
	t.Run("everything agrees with each other but not with the configuration", func(t *testing.T) {
		s := gliStartServer(t, func(c *ingest.Config) { c.ClusterID = "lab-b" })
		n := s.AddNode(t, "cfg") // certificate cluster = lab-b (configured)
		n.Cert = s.PKI.NodeCert(t, "lab-a", n.UID)
		st, _ := s.Stream(t, n.Cert)
		h := s.HelloMsg(n)
		h.ClusterId = "lab-a"
		_, err := gliwHello(st, h)
		gliwWantCode(t, "cert=hello=lab-a, configured lab-b", err, codes.PermissionDenied)
	})
}

// TestGLI025_UnknownNodeAndUIDMismatchAreIndistinguishable: the response must not
// say whether a node name exists (same code, same message).
func TestGLI025_UnknownNodeAndUIDMismatchAreIndistinguishable(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "indist")
	peer := s.AddNode(t, "realother")
	secretName := "GLIWMARK-existing-node"
	s.Nodes.Put(secretName, peer.UID)

	ask := func(what string, mut func(h *ingestpb.ClientHello)) error {
		t.Helper()
		s.Clock.Advance(10 * time.Second) // keep the hello bucket out of the way
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		h := s.HelloMsg(n)
		mut(h)
		_, err := gliwHello(st, h)
		gliwWantCode(t, what, err, codes.PermissionDenied, "GLIWMARK", n.UID, peer.UID, "ghost")
		return err
	}
	unknown := ask("unknown node name", func(h *ingestpb.ClientHello) { h.NodeName = "gliw-ghost-node" })
	mismatch := ask("existing node with another uid", func(h *ingestpb.ClientHello) { h.NodeName = secretName })

	s.Nodes.SetErr(fmt.Errorf("directory said: %w", liveingest.ErrNodeNotFound))
	wrapped := ask("wrapped ErrNodeNotFound", func(h *ingestpb.ClientHello) {})
	s.Nodes.SetErr(nil)

	if gliwMessage(unknown) != gliwMessage(mismatch) {
		t.Errorf("messages differ: unknown=%q uid-mismatch=%q", gliwMessage(unknown), gliwMessage(mismatch))
	}
	if gliwMessage(unknown) != gliwMessage(wrapped) {
		t.Errorf("messages differ: unknown=%q wrapped=%q", gliwMessage(unknown), gliwMessage(wrapped))
	}
	if c := s.Sessions.Calls(); len(c) != 0 {
		t.Errorf("session store called: %+v", c)
	}
}

func TestGLI030_NodeDirectoryFailures(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "dir")

	t.Run("other directory error is Unavailable", func(t *testing.T) {
		s.Nodes.SetErr(errors.New("GLIWMARK-directory-body connection refused"))
		defer s.Nodes.SetErr(nil)
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "directory failure", err, codes.Unavailable, "GLIWMARK", "refused")
		if c := s.Sessions.Calls(); len(c) != 0 {
			t.Errorf("session store called after a directory failure: %+v", c)
		}
	})
	t.Run("a call that blocks is cut at five seconds", func(t *testing.T) {
		s.Clock.Advance(10 * time.Second)
		s.Nodes.SetDelay(time.Hour)
		defer s.Nodes.SetDelay(0)
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		start := time.Now()
		_, err := gliwHello(st, s.HelloMsg(n))
		took := time.Since(start)
		gliwWantCode(t, "blocked directory", err, codes.Unavailable)
		if took < 4500*time.Millisecond {
			t.Errorf("the hello failed after %v, want about 5 s (the cap must not be shorter)", took)
		}
		if took > 12*time.Second {
			t.Errorf("the hello failed after %v, want about 5 s", took)
		}
		if c := s.Sessions.Calls(); len(c) != 0 {
			t.Errorf("session store called after a directory timeout: %+v", c)
		}
	})
	t.Run("recovers once the directory answers", func(t *testing.T) {
		s.Clock.Advance(10 * time.Second)
		_, _, h := s.Connect(t, n)
		if h.GetSession() != 1 {
			t.Errorf("session = %d, want 1 (failed hellos allocate nothing)", h.GetSession())
		}
	})
}

// TestGLI100_SessionStoreErrorMapping: GLI-100 rows for store errors; the error
// body (marker) never reaches the status message or the log.
func TestGLI100_SessionStoreErrorMapping(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "map")
	const marker = "GLIWMARK-store-body"
	table := []struct {
		name     string
		sentinel error
		want     codes.Code
	}{
		{"ErrNodeNotManaged", liveingest.ErrNodeNotManaged, codes.FailedPrecondition},
		{"ErrSessionOverflow", liveingest.ErrSessionOverflow, codes.ResourceExhausted},
		{"ErrSessionConflict", liveingest.ErrSessionConflict, codes.Aborted},
		{"ErrStoreUnavailable", liveingest.ErrStoreUnavailable, codes.Unavailable},
	}
	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			s.Clock.Advance(10 * time.Second)
			s.Sessions.SetErr(fmt.Errorf("%s: %w", marker, tc.sentinel))
			defer s.Sessions.SetErr(nil)
			st, closeFn := s.Stream(t, n.Cert)
			defer closeFn()
			_, err := gliwHello(st, s.HelloMsg(n))
			gliwWantCode(t, "hello", err, tc.want, "GLIWMARK", marker)
		})
	}
	if got := s.Sessions.Stored(n.UID); got != 0 {
		t.Errorf("stored session = %d after only failed hellos", got)
	}
	s.Clock.Advance(10 * time.Second)
	_, _, h := s.Connect(t, n)
	if h.GetSession() != 1 {
		t.Errorf("session after the failures = %d, want 1", h.GetSession())
	}
	if logs := s.LogText(); strings.Contains(logs, "GLIWMARK") {
		t.Errorf("the server log contains the store error body")
	}
}

// TestGLI030_NodeNotManagedConsumesHelloBucket: a hello that fails at
// the store still used its token, so retrying without delay hits the rate limit.
func TestGLI030_NodeNotManagedConsumesHelloBucket(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "notmanaged")
	s.Sessions.SetErr(liveingest.ErrNodeNotManaged)
	for i := 0; i < 3; i++ {
		st, closeFn := s.Stream(t, n.Cert)
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, fmt.Sprintf("attempt %d", i+1), err, codes.FailedPrecondition)
		closeFn()
	}
	st, closeFn := s.Stream(t, n.Cert)
	defer closeFn()
	_, err := gliwHello(st, s.HelloMsg(n))
	gliwWantCode(t, "fourth attempt at the same instant", err, codes.ResourceExhausted)
	if c := s.Sessions.Calls(); len(c) != 3 {
		t.Errorf("store calls = %d, want 3 (the rate limit sits before the store)", len(c))
	}
}

// TestGLI031_SessionNumbering: the server hands the store the highest session it
// has issued for the node in this process as `after`, and a failed allocation
// does not move it.
func TestGLI031_SessionNumbering(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "num")
	hello := func() (int64, error) {
		s.Clock.Advance(10 * time.Second)
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		h, err := gliwHello(st, s.HelloMsg(n))
		if err != nil {
			return 0, err
		}
		return h.GetSession(), nil
	}
	for want := int64(1); want <= 2; want++ {
		got, err := hello()
		if err != nil || got != want {
			t.Fatalf("hello %d: session %d, err %v", want, got, err)
		}
	}
	// The persisted value is lost (NodePathState deleted and recreated) while the
	// process stays up: the server's own memory keeps the session strictly increasing.
	s.Sessions.SetStored(n.UID, 0)
	if got, err := hello(); err != nil || got != 3 {
		t.Fatalf("after losing the stored value: session %d, err %v, want 3", got, err)
	}
	s.Sessions.SetErr(liveingest.ErrStoreUnavailable)
	if _, err := hello(); gliwCode(err) != codes.Unavailable {
		t.Fatalf("failing hello: %v, want Unavailable", err)
	}
	s.Sessions.SetErr(nil)
	if got, err := hello(); err != nil || got != 4 {
		t.Fatalf("after the failure: session %d, err %v, want 4", got, err)
	}
	calls := s.Sessions.CallsFor(n.UID)
	wantAfter := []int64{0, 1, 2, 3, 3}
	wantResult := []int64{1, 2, 3, 0, 4}
	if len(calls) != len(wantAfter) {
		t.Fatalf("store calls = %+v, want %d calls", calls, len(wantAfter))
	}
	for i, c := range calls {
		if c.After != wantAfter[i] || c.Result != wantResult[i] {
			t.Errorf("call %d = {After:%d Result:%d Err:%v}, want After %d Result %d", i, c.After, c.Result, c.Err, wantAfter[i], wantResult[i])
		}
	}
	// A different node starts from zero.
	m := s.AddNode(t, "num2")
	_, _, h := s.Connect(t, m)
	if c := s.Sessions.CallsFor(m.UID); len(c) != 1 || c[0].After != 0 || h.GetSession() != 1 {
		t.Errorf("second node: calls %+v session %d, want After 0 session 1", c, h.GetSession())
	}
}

// TestGLI034_ConcurrentHellosSameNode (GLI-124 (b)): three simultaneous hellos for
// one node are serialised; every session differs and only the highest session's
// stream survives.
func TestGLI034_ConcurrentHellosSameNode(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "conc")
	s.Sessions.SetDelay(150 * time.Millisecond)

	type result struct {
		idx     int
		session int64
		err     error
	}
	streams := make([]ingestpb.Ingest_StreamClient, 3)
	for i := range streams {
		streams[i], _ = s.Stream(t, n.Cert)
	}
	results := make(chan result, len(streams))
	var wg sync.WaitGroup
	for i := range streams {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, err := gliwHello(streams[i], s.HelloMsg(n))
			r := result{idx: i, err: err}
			if err == nil {
				r.session = h.GetSession()
			}
			results <- r
		}(i)
	}
	wg.Wait()
	close(results)
	s.Sessions.SetDelay(0)

	sessions := map[int64]int{}
	winner, top := -1, int64(0)
	for r := range results {
		if r.err != nil {
			t.Fatalf("hello %d failed: %v", r.idx, r.err)
		}
		if prev, dup := sessions[r.session]; dup {
			t.Fatalf("streams %d and %d got the same session %d", prev, r.idx, r.session)
		}
		sessions[r.session] = r.idx
		if r.session > top {
			top, winner = r.session, r.idx
		}
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %v, want three distinct values", sessions)
	}
	calls := s.Sessions.CallsFor(n.UID)
	if len(calls) != 3 {
		t.Fatalf("store calls = %+v, want 3", calls)
	}
	var prev int64
	for i, c := range calls {
		if c.After != prev || c.Result <= c.After {
			t.Errorf("call %d = %+v: the hellos of one node must be serialised (After must equal the previous Result)", i, c)
		}
		prev = c.Result
	}
	for i, st := range streams {
		if i == winner {
			continue
		}
		gliwExpectEnd(t, fmt.Sprintf("superseded stream %d", i), st, codes.Aborted)
	}
	s.Baseline(t, streams[winner], n, top)
	s.WantSnapshot(t, "winner baseline", n, top, 0)
}

// TestGLI034_FencedStream: after a newer hello succeeds the old stream is ended
// with Aborted and nothing it sends changes the state.
func TestGLI034_FencedStream(t *testing.T) {
	t.Run("frame sent after the fence", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "fence")
		a, _, ha := s.Connect(t, n)
		s.Baseline(t, a, n, ha.GetSession())
		s.WantSnapshot(t, "stream A baseline", n, ha.GetSession(), 0)

		b, _, hb := s.Connect(t, n)
		if hb.GetSession() <= ha.GetSession() {
			t.Fatalf("new session %d is not greater than %d", hb.GetSession(), ha.GetSession())
		}
		s.WantNoObservation(t, "right after the new ServerHello (GLI-035)", n)
		s.Baseline(t, b, n, hb.GetSession())

		// The fenced stream guesses the new session and sends the next frame.
		next := gliwFrame(t, n, hb.GetSession(), 1)
		if err := gliwSend(a, next); err != nil {
			t.Fatalf("send on the fenced stream: %v", err)
		}
		for {
			fr, err := a.Recv()
			if err != nil {
				gliwWantCode(t, "fenced stream", err, codes.Aborted)
				break
			}
			if ack := fr.GetAck(); ack != nil && (ack.GetCode() == ingestpb.AckCode_ACCEPTED || ack.GetCode() == ingestpb.AckCode_DUPLICATE) {
				t.Fatalf("the fenced stream got ack %v", ack.GetCode())
			}
		}
		s.WantSnapshot(t, "after the fenced stream's frame", n, hb.GetSession(), 0)

		// If the fenced copy had been applied this identical frame would be a DUPLICATE.
		s.Clock.Advance(10 * time.Second)
		ack := gliwExchange(t, b, next)
		if ack.GetCode() != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("sequence 1 on the current stream: ack %v, want ACCEPTED", ack.GetCode())
		}
		s.WantSnapshot(t, "after the current stream's frame", n, hb.GetSession(), 1)
	})

	t.Run("frame in flight while the new hello is being processed", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "fence2")
		a, _, ha := s.Connect(t, n)
		s.Baseline(t, a, n, ha.GetSession())

		s.Sessions.SetDelay(400 * time.Millisecond)
		b, _ := s.Stream(t, n.Cert)
		done := make(chan error, 1)
		var hb *ingestpb.ServerHello
		go func() {
			var err error
			hb, err = gliwHello(b, s.HelloMsg(n))
			done <- err
		}()
		time.Sleep(100 * time.Millisecond)
		// Whatever the server does with this frame, once the hello has succeeded
		// the old observation is gone (GLI-035) and stream A is Aborted.
		_ = gliwSend(a, gliwFrame(t, n, ha.GetSession(), 1))
		if err := <-done; err != nil {
			t.Fatalf("hello on stream B: %v", err)
		}
		s.Sessions.SetDelay(0)
		s.WantNoObservation(t, "after the new hello", n)
		for {
			fr, err := a.Recv()
			if err != nil {
				gliwWantCode(t, "fenced stream", err, codes.Aborted)
				break
			}
			_ = fr
		}
		s.WantNoObservation(t, "after stream A ended", n)
		if hb.GetSession() <= ha.GetSession() {
			t.Errorf("sessions %d then %d", ha.GetSession(), hb.GetSession())
		}
	})
}

func TestGLI035_NewSessionDiscardsObservation(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "discard")
	a, closeA, ha := s.Connect(t, n)
	s.Baseline(t, a, n, ha.GetSession())
	s.WantSnapshot(t, "first session", n, ha.GetSession(), 0)

	_, closeB, hb := s.Connect(t, n)
	_ = closeB
	s.WantNoObservation(t, "second hello, before its baseline", n)
	closeA()
	s.WantNoObservation(t, "after the old stream closed", n)

	b, _ := s.Stream(t, n.Cert) // a third stream: hello again to get a clean handle
	s.Clock.Advance(10 * time.Second)
	hc, err := gliwHello(b, s.HelloMsg(n))
	if err != nil {
		t.Fatalf("third hello: %v", err)
	}
	if hc.GetSession() <= hb.GetSession() {
		t.Fatalf("sessions %d then %d", hb.GetSession(), hc.GetSession())
	}
	s.Baseline(t, b, n, hc.GetSession())
	s.WantSnapshot(t, "third session baseline", n, hc.GetSession(), 0)
}

// TestGLI030_FailedHelloHasNoSideEffect: a hello that fails at any step neither
// allocates a session nor disturbs the node's current stream and observation.
func TestGLI030_FailedHelloHasNoSideEffect(t *testing.T) {
	s := gliStartServer(t)
	n := s.AddNode(t, "noside")
	a, _, ha := s.Connect(t, n)
	session := ha.GetSession()
	s.Baseline(t, a, n, session)
	callsBefore := len(s.Sessions.CallsFor(n.UID))

	fail := func(what string, want codes.Code, mut func(h *ingestpb.ClientHello), prep func() func()) {
		t.Helper()
		s.Clock.Advance(10 * time.Second)
		if prep != nil {
			defer prep()()
		}
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		h := s.HelloMsg(n)
		mut(h)
		_, err := gliwHello(st, h)
		gliwWantCode(t, what, err, want)
		s.WantSnapshot(t, "after "+what, n, session, 0)
	}
	noMut := func(h *ingestpb.ClientHello) {}
	fail("version mismatch", codes.InvalidArgument, func(h *ingestpb.ClientHello) { h.Version = "v9" }, nil)
	fail("cluster mismatch", codes.PermissionDenied, func(h *ingestpb.ClientHello) { h.ClusterId = "lab-z" }, nil)
	fail("unknown node name", codes.PermissionDenied, func(h *ingestpb.ClientHello) { h.NodeName = "gliw-ghost-node" }, nil)
	fail("store unavailable", codes.Unavailable, noMut, func() func() {
		s.Sessions.SetErr(liveingest.ErrStoreUnavailable)
		return func() { s.Sessions.SetErr(nil) }
	})
	fail("store overflow", codes.ResourceExhausted, noMut, func() func() {
		s.Sessions.SetErr(liveingest.ErrSessionOverflow)
		return func() { s.Sessions.SetErr(nil) }
	})
	fail("session conflict", codes.Aborted, noMut, func() func() {
		s.Sessions.SetErr(liveingest.ErrSessionConflict)
		return func() { s.Sessions.SetErr(nil) }
	})

	// The first stream is still the active one: it is accepted and the session
	// numbering did not move.
	s.Clock.Advance(10 * time.Second)
	ack := gliwExchange(t, a, gliwFrame(t, n, session, 1))
	if ack.GetCode() != ingestpb.AckCode_ACCEPTED {
		t.Fatalf("stream A after the failed hellos: ack %v, want ACCEPTED", ack.GetCode())
	}
	if got := s.Sessions.Stored(n.UID); got != session {
		t.Errorf("stored session = %d, want %d", got, session)
	}
	for _, c := range s.Sessions.CallsFor(n.UID)[callsBefore:] {
		if c.Err == nil {
			t.Errorf("a failed hello recorded a successful allocation: %+v", c)
		}
	}
}

// TestGLI036_SessionOverflow: MaxInt64 refuses the hello without side effect.
func TestGLI036_SessionOverflow(t *testing.T) {
	t.Run("first hello of a node", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "ovfirst")
		s.Sessions.SetStored(n.UID, 1<<63-1)
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "overflow hello", err, codes.ResourceExhausted)
		s.WantNoObservation(t, "after the refused hello", n)
		if got := s.Sessions.Stored(n.UID); got != 1<<63-1 {
			t.Errorf("stored session changed to %d", got)
		}
	})
	t.Run("active node keeps its stream and observation", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "ovactive")
		a, _, ha := s.Connect(t, n)
		s.Baseline(t, a, n, ha.GetSession())
		s.Sessions.SetStored(n.UID, 1<<63-1)
		s.Clock.Advance(10 * time.Second)
		st, closeFn := s.Stream(t, n.Cert)
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "overflow hello on an active node", err, codes.ResourceExhausted)
		closeFn()
		s.WantSnapshot(t, "after the refused hello", n, ha.GetSession(), 0)
		ack := gliwExchange(t, a, gliwFrame(t, n, ha.GetSession(), 1))
		if ack.GetCode() != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("the old stream was disturbed: ack %v", ack.GetCode())
		}
		s.WantSnapshot(t, "old stream still works", n, ha.GetSession(), 1)
	})
	t.Run("a stored value below the maximum still allocates", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "ovnear")
		s.Sessions.SetStored(n.UID, 1<<63-2)
		_, _, h := s.Connect(t, n)
		if h.GetSession() != 1<<63-1 {
			t.Errorf("session = %d, want MaxInt64", h.GetSession())
		}
		s.Clock.Advance(10 * time.Second)
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "hello after MaxInt64 was issued", err, codes.ResourceExhausted)
	})
}

func TestGLI037_IdleTimeout(t *testing.T) {
	const idle = 2 * time.Second
	s := gliStartServer(t, gliwLimits(ingest.Limits{IdleTimeout: idle}))

	t.Run("before the baseline", func(t *testing.T) {
		n := s.AddNode(t, "idle1")
		st, _, _ := s.Connect(t, n)
		start := time.Now()
		_, err := st.Recv()
		took := time.Since(start)
		gliwWantCode(t, "idle stream", err, codes.DeadlineExceeded)
		if took < 1500*time.Millisecond || took > idle+3500*time.Millisecond {
			t.Errorf("idle stream ended after %v, want about %v (+2 s load margin)", took, idle)
		}
	})
	t.Run("after the baseline, state is kept", func(t *testing.T) {
		n := s.AddNode(t, "idle2")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		start := time.Now()
		_, err := st.Recv()
		took := time.Since(start)
		gliwWantCode(t, "idle stream after baseline", err, codes.DeadlineExceeded)
		if took < 1500*time.Millisecond || took > idle+3500*time.Millisecond {
			t.Errorf("idle stream ended after %v, want about %v (+2 s load margin)", took, idle)
		}
		s.WantSnapshot(t, "after the idle timeout", n, h.GetSession(), 0)
	})
	t.Run("every frame restarts the timer", func(t *testing.T) {
		n := s.AddNode(t, "idle3")
		st, _, h := s.Connect(t, n)
		f0 := gliwFrame(t, n, h.GetSession(), 0)
		if ack := gliwExchange(t, st, f0); ack.GetCode() != ingestpb.AckCode_ACCEPTED {
			t.Fatalf("baseline ack %v", ack.GetCode())
		}
		for i := 0; i < 2; i++ {
			time.Sleep(1200 * time.Millisecond) // < idle, but the sum exceeds it
			ack := gliwExchange(t, st, f0)
			if ack.GetCode() != ingestpb.AckCode_DUPLICATE {
				t.Fatalf("resend %d: ack %v, want DUPLICATE (the stream must still be open)", i+1, ack.GetCode())
			}
		}
		start := time.Now()
		_, err := st.Recv()
		took := time.Since(start)
		gliwWantCode(t, "idle after the last frame", err, codes.DeadlineExceeded)
		if took < 1500*time.Millisecond {
			t.Errorf("the stream ended %v after the last frame, want about %v", took, idle)
		}
	})
}

func TestGLI037_LeaderLoss(t *testing.T) {
	t.Run("NodeBundles observes the loss and the state stays gone", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "lead1")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		s.WantObserved(t, "leader", n)

		s.Leader.Set(false)
		s.WantNoObservation(t, "leadership lost", n)
		s.Leader.Set(true)
		s.WantNoObservation(t, "leadership regained, no new baseline yet", n)

		// A new hello and baseline are needed.
		s.Clock.Advance(10 * time.Second)
		st2, _, h2 := s.Connect(t, n)
		if h2.GetSession() <= h.GetSession() {
			t.Fatalf("sessions %d then %d", h.GetSession(), h2.GetSession())
		}
		s.WantNoObservation(t, "regained leadership and new hello, no baseline", n)
		s.Baseline(t, st2, n, h2.GetSession())
		s.WantSnapshot(t, "new baseline", n, h2.GetSession(), 0)
	})
	t.Run("open stream ends Unavailable at the next frame while not leading", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "lead2")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		s.Leader.Set(false)
		s.WantNoObservation(t, "leadership lost", n) // the engine observes the loss
		s.Clock.Advance(10 * time.Second)
		if err := gliwSend(st, gliwFrame(t, n, h.GetSession(), 1)); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "frame while not leading", st, codes.Unavailable)
	})
	t.Run("frame processing itself checks leadership and drops the state", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "lead3")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		s.Leader.Set(false)
		s.Clock.Advance(10 * time.Second)
		if err := gliwSend(st, gliwFrame(t, n, h.GetSession(), 1)); err != nil {
			t.Fatalf("send: %v", err)
		}
		gliwExpectEnd(t, "frame while not leading", st, codes.Unavailable)
		s.Leader.Set(true)
		s.WantNoObservation(t, "leadership regained after a frame saw the loss", n)
	})
	t.Run("hello is refused while not leading and touches nothing", func(t *testing.T) {
		s := gliStartServer(t)
		n := s.AddNode(t, "lead4")
		s.Leader.Set(false)
		st, closeFn := s.Stream(t, n.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(n))
		gliwWantCode(t, "hello on a non-leader", err, codes.Unavailable)
		if c := s.Sessions.Calls(); len(c) != 0 {
			t.Errorf("session store called by a non-leader: %+v", c)
		}
		s.Leader.Set(true)
		s.Clock.Advance(10 * time.Second)
		_, _, h := s.Connect(t, n)
		if h.GetSession() != 1 {
			t.Errorf("session after regaining leadership = %d, want 1", h.GetSession())
		}
	})
	t.Run("a nil LeaderGate means always leader", func(t *testing.T) {
		s := gliStartServer(t, func(c *ingest.Config) { c.Leader = nil })
		n := s.AddNode(t, "lead5")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		s.WantObserved(t, "nil leader gate", n)
	})
}

func TestGLI053_MaxStreams(t *testing.T) {
	t.Run("authenticated streams before the hello are counted", func(t *testing.T) {
		s := gliStartServer(t, gliwLimits(ingest.Limits{MaxStreams: 2}))
		n1, n2, n3 := s.AddNode(t, "ms1"), s.AddNode(t, "ms2"), s.AddNode(t, "ms3")
		_, closeA := s.Stream(t, n1.Cert)
		_, closeB := s.Stream(t, n2.Cert)
		time.Sleep(700 * time.Millisecond) // let both handlers start; they wait for a hello
		c, closeC := s.Stream(t, n3.Cert)
		_, err := gliwHello(c, s.HelloMsg(n3))
		gliwWantCode(t, "third stream", err, codes.ResourceExhausted)
		closeC()
		if calls := s.Sessions.Calls(); len(calls) != 0 {
			t.Errorf("a refused stream reached the store: %+v", calls)
		}
		closeA()
		gliwEventually(t, 20*time.Second, "a slot frees up when a stream closes", func() bool {
			st, closeFn := s.Stream(t, n3.Cert)
			defer closeFn()
			_, err := gliwHello(st, s.HelloMsg(n3))
			return err == nil
		})
		closeB()
	})
	t.Run("the stream itself counts (limit 1)", func(t *testing.T) {
		s := gliStartServer(t, gliwLimits(ingest.Limits{MaxStreams: 1}))
		n1, n2 := s.AddNode(t, "one1"), s.AddNode(t, "one2")
		_, _, _ = s.Connect(t, n1)
		st, closeFn := s.Stream(t, n2.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(n2))
		gliwWantCode(t, "second stream with MaxStreams=1", err, codes.ResourceExhausted)
	})
	t.Run("a stream that ends by HelloTimeout frees its slot", func(t *testing.T) {
		s := gliStartServer(t, gliwLimits(ingest.Limits{MaxStreams: 1, HelloTimeout: time.Second}))
		n1, n2 := s.AddNode(t, "ht1"), s.AddNode(t, "ht2")
		silent, _ := s.Stream(t, n1.Cert)
		_, err := silent.Recv()
		gliwWantCode(t, "silent stream", err, codes.DeadlineExceeded)
		gliwEventually(t, 20*time.Second, "the slot of the timed-out stream is free", func() bool {
			st, closeFn := s.Stream(t, n2.Cert)
			defer closeFn()
			_, err := gliwHello(st, s.HelloMsg(n2))
			return err == nil
		})
	})
	t.Run("a hello blocked in the store still counts", func(t *testing.T) {
		s := gliStartServer(t, gliwLimits(ingest.Limits{MaxStreams: 1}))
		n1, n2 := s.AddNode(t, "blk1"), s.AddNode(t, "blk2")
		s.Sessions.SetDelay(time.Hour)
		a, _ := s.Stream(t, n1.Cert)
		go func() { _, _ = gliwHello(a, s.HelloMsg(n1)) }()
		gliwEventually(t, 10*time.Second, "the first hello reached the store", func() bool { return s.Sessions.InFlight() == 1 })
		st, closeFn := s.Stream(t, n2.Cert)
		defer closeFn()
		_, err := gliwHello(st, s.HelloMsg(n2))
		gliwWantCode(t, "second stream while the first hello is pending", err, codes.ResourceExhausted)
	})
}

// TestGLI100_ClientEndsKeepTheObservation: a clean half-close ends the stream with
// OK and a cancelled stream ends too; neither discards the node's observation
// (it lives until NodeRetention, GLI-076).
func TestGLI100_ClientEndsKeepTheObservation(t *testing.T) {
	s := gliStartServer(t)
	t.Run("CloseSend", func(t *testing.T) {
		n := s.AddNode(t, "close")
		st, _, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		if err := st.CloseSend(); err != nil {
			t.Fatalf("CloseSend: %v", err)
		}
		fr, err := st.Recv()
		if err == nil {
			t.Fatalf("got a server message (%v), want the stream to end with OK", fr)
		}
		if got := gliwCode(err); got != codes.OK {
			t.Fatalf("status after a clean half-close = %v (%v), want OK", got, err)
		}
		s.WantSnapshot(t, "after the half-close", n, h.GetSession(), 0)
	})
	t.Run("cancel", func(t *testing.T) {
		n := s.AddNode(t, "cancel")
		st, closeFn, h := s.Connect(t, n)
		s.Baseline(t, st, n, h.GetSession())
		closeFn()
		time.Sleep(300 * time.Millisecond) // let the server notice the cancellation
		s.WantSnapshot(t, "after the client cancelled", n, h.GetSession(), 0)
		// The node can reconnect with a new session.
		s.Clock.Advance(10 * time.Second)
		_, closeFn2, h2 := s.Connect(t, n)
		closeFn2()
		if h2.GetSession() <= h.GetSession() {
			t.Errorf("sessions %d then %d", h.GetSession(), h2.GetSession())
		}
	})
}

// TestGLI037_IdleTimeoutChecksLeadership: the IdleTimeout expiry point is one of
// the places where the engine asks the LeaderGate. A false answer there discards
// the node's observation and ends the stream with Unavailable instead of
// DeadlineExceeded. Leadership is regained before anything reads the state and no
// frame or NodeBundles call happens while it is lost, so only the check at the
// expiry point can have discarded the observation.
func TestGLI037_IdleTimeoutChecksLeadership(t *testing.T) {
	const idle = 2 * time.Second
	s := gliStartServer(t, gliwLimits(ingest.Limits{IdleTimeout: idle}))
	n := s.AddNode(t, "idlelead")
	st, _, h := s.Connect(t, n)
	s.Baseline(t, st, n, h.GetSession())
	s.WantSnapshot(t, "before leadership is lost", n, h.GetSession(), 0)

	s.Leader.Set(false)
	start := time.Now()
	_, err := st.Recv()
	took := time.Since(start)
	gliwWantCode(t, "idle stream while not leading", err, codes.Unavailable)
	if took < 1500*time.Millisecond || took > idle+3500*time.Millisecond {
		t.Errorf("the stream ended after %v, want about IdleTimeout %v (+2 s load margin)", took, idle)
	}

	s.Leader.Set(true)
	gliwEventually(t, 2*time.Second, "the observation was discarded at the IdleTimeout expiry point", func() bool {
		return s.Observed(t, n) != nil
	})
	s.WantNoObservation(t, "leadership regained, no new hello and baseline yet", n)

	// A new hello and baseline restore the observation.
	s.Clock.Advance(10 * time.Second)
	st2, _, h2 := s.Connect(t, n)
	if h2.GetSession() <= h.GetSession() {
		t.Fatalf("sessions %d then %d", h.GetSession(), h2.GetSession())
	}
	s.Baseline(t, st2, n, h2.GetSession())
	s.WantSnapshot(t, "new baseline", n, h2.GetSession(), 0)
}

// TestGLI030_SessionStoreCallIsCutAtTwentySeconds: a SessionStore call that never
// answers (it only honours ctx) ends the hello after about 20 seconds with the
// status of a store failure (GLI-030 (8), GLI-100); the call is cancelled, nothing
// is stored and the node can say hello again. The wait is real time, so the test
// is skipped with -short (GLI-125).
func TestGLI030_SessionStoreCallIsCutAtTwentySeconds(t *testing.T) {
	if testing.Short() {
		t.Skip("waits about 20 seconds in real time; skipped with -short (GLI-125)")
	}
	s := gliStartServer(t)
	n := s.AddNode(t, "storecap")
	s.Sessions.SetDelay(time.Hour)
	st, closeFn := s.Stream(t, n.Cert)
	defer closeFn()

	start := time.Now()
	_, err := gliwHello(st, s.HelloMsg(n))
	took := time.Since(start)
	gliwWantCode(t, "hello with a store that never answers", err, codes.Unavailable)
	if took < 19*time.Second {
		t.Errorf("the hello failed after %v, want about 20 s (the cap must not be shorter)", took)
	}
	if took > 24*time.Second {
		t.Errorf("the hello failed after %v, want about 20 s (+2 s load margin; the agent waits 30 s for the ServerHello)", took)
	}
	calls := s.Sessions.CallsFor(n.UID)
	if len(calls) != 1 || calls[0].Err == nil || calls[0].Result != 0 {
		t.Errorf("session store calls = %+v, want one cancelled call", calls)
	}
	if got := s.Sessions.InFlight(); got != 0 {
		t.Errorf("%d store call(s) still running after the hello ended", got)
	}
	if got := s.Sessions.Stored(n.UID); got != 0 {
		t.Errorf("stored session = %d after a cancelled allocation", got)
	}
	s.WantNoObservation(t, "after the failed hello", n)

	s.Sessions.SetDelay(0)
	s.Clock.Advance(10 * time.Second)
	_, _, h := s.Connect(t, n)
	if h.GetSession() != 1 {
		t.Errorf("session after the failed hello = %d, want 1", h.GetSession())
	}
}
