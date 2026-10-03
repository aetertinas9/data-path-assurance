package liveingestenv_test

// The rig: the real components of gpu-fleet-live-ingest wired together against
// the envtest API server - the Kubernetes adapter (SessionStore, NodeDirectory,
// LeaderGate), the ingest server, the S3a status controller running in-process
// with NewLiveAssessor(server.BundleSource()), and the agent library client with
// a BASE sysfs fixture and a fake nvidia-smi (GLI-127 clock rules: one fake
// Clock for server, controller and agent, moved >= 10 s per frame; the adapter
// keeps the system clock because the Lease times are real).

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aetertinas9/data-path-assurance/internal/agent/liveclient"
	"github.com/aetertinas9/data-path-assurance/internal/app"
	"github.com/aetertinas9/data-path-assurance/internal/fleet"
	"github.com/aetertinas9/data-path-assurance/internal/ingest"
	"github.com/aetertinas9/data-path-assurance/internal/ingest/ingestpb"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/api/v1alpha1"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/controller"
	"github.com/aetertinas9/data-path-assurance/internal/kubernetes/ingestadapter"
)

// ---------------------------------------------------------------------------
// the Kubernetes adapter
// ---------------------------------------------------------------------------

type glieAdapter struct {
	A      *ingestadapter.Adapter
	Rec    *glieRecorder
	Cfg    *rest.Config
	cancel context.CancelFunc
	fin    chan struct{}
	err    error
	mu     sync.Mutex
}

type glieAdapterOpts struct {
	ControllerID, LeaseName  string
	PollInterval, StaleAfter time.Duration // defaults 100 ms and 2 s
	NoPoll                   bool          // do not start RunLeaderPoll
	Base                     *rest.Config  // default: the envtest config
	Mutate                   func(*ingestadapter.Options)
}

// glieNewAdapter builds an adapter on a recording copy of the envtest config
// and, unless NoPoll, runs its leader poll until the test ends.
func glieNewAdapter(t *testing.T, e *glieEnvT, o glieAdapterOpts) *glieAdapter {
	t.Helper()
	base := o.Base
	if base == nil {
		base = e.Config
	}
	cfg, rec := glieRecordingConfig(base)
	pi, sa := o.PollInterval, o.StaleAfter
	if pi == 0 {
		pi = 100 * time.Millisecond
	}
	if sa == 0 {
		sa = 2 * time.Second
	}
	opts := ingestadapter.Options{
		RESTConfig: cfg, ControllerID: o.ControllerID, LeaseNamespace: glieNS, LeaseName: o.LeaseName,
		PollInterval: pi, StaleAfter: sa,
	}
	if o.Mutate != nil {
		o.Mutate(&opts)
	}
	ad, err := ingestadapter.New(opts)
	if err != nil {
		t.Fatalf("ingestadapter.New: %v", err)
	}
	r := &glieAdapter{A: ad, Rec: rec, Cfg: cfg}
	t.Cleanup(func() { _ = r.StopPoll(t) })
	if !o.NoPoll {
		r.StartPoll(t)
	}
	return r
}

// StartPoll runs RunLeaderPoll in a goroutine (once).
func (r *glieAdapter) StartPoll(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fin != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.fin = make(chan struct{})
	fin := r.fin
	go func() {
		err := r.A.RunLeaderPoll(ctx)
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(fin)
	}()
}

// StopPoll cancels RunLeaderPoll and returns its result (GLI-113: nil); it fails
// the test when it does not return within 10 s.
func (r *glieAdapter) StopPoll(t *testing.T) error {
	t.Helper()
	r.mu.Lock()
	cancel, fin := r.cancel, r.fin
	r.mu.Unlock()
	if fin == nil {
		return nil
	}
	cancel()
	select {
	case <-fin:
	case <-time.After(10 * time.Second):
		t.Errorf("GLI-113: RunLeaderPoll did not return within 10 s after its context was cancelled")
		return errors.New("RunLeaderPoll did not return")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *glieAdapter) Leading() bool { return r.A.Leader().Leading() }

// WaitLeading waits until Leader().Leading() reports want.
func (r *glieAdapter) WaitLeading(t testing.TB, want bool, timeout time.Duration) {
	t.Helper()
	glieEventually(t, timeout, func() (bool, string) {
		return r.Leading() == want, fmt.Sprintf("Leading() = %v, want %v", r.Leading(), want)
	})
}

func glieNodeRef(node *corev1.Node) fleet.NodeRef {
	return fleet.NodeRef{ClusterID: glieCluster, Name: node.Name, UID: string(node.UID)}
}

func (r *glieAdapter) Alloc(ctx context.Context, node *corev1.Node, after int64) (int64, error) {
	return r.A.Sessions().AllocateSession(ctx, glieNodeRef(node), after)
}

// ---------------------------------------------------------------------------
// the ingest server
// ---------------------------------------------------------------------------

type glieServerRig struct {
	Srv        *ingest.Server
	Addr       string
	PKI        *gliePKI
	ServerLeaf glieLeaf
	Dir        string
	CAFile     string
	CertFile   string
	KeyFile    string
	Logs       *glieSyncBuf
	cancel     context.CancelFunc
	done       chan error
	once       sync.Once
	err        error
}

// glieStartServer builds an ingest server that uses the adapter as node
// directory, session store and leader gate, and serves it on 127.0.0.1:0.
func glieStartServer(t *testing.T, ad *glieAdapter, pki *gliePKI, mutate func(*ingest.Config)) *glieServerRig {
	t.Helper()
	s := &glieServerRig{PKI: pki, Logs: &glieSyncBuf{}, Dir: t.TempDir()}
	s.ServerLeaf = pki.Server(t)
	s.CAFile = filepath.Join(s.Dir, "ca.pem")
	s.CertFile = filepath.Join(s.Dir, "server.crt")
	s.KeyFile = filepath.Join(s.Dir, "server.key")
	glieWriteFile(t, s.CAFile, string(pki.CAPEM), 0o644)
	glieInstall(t, s.CertFile, s.KeyFile, s.ServerLeaf)
	cfg := ingest.Config{
		ClusterID: glieCluster, ServiceDNS: "localhost", CollectorProfileID: "live:glie",
		TrustedSources: ingest.DefaultTrustedSources(),
		TLS:            ingest.TLSFiles{CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile},
		Nodes:          ad.A.Nodes(), Sessions: ad.A.Sessions(), Leader: ad.A.Leader(),
		Logger: glieLogger(s.Logs),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := ingest.NewServer(cfg)
	if err != nil {
		t.Fatalf("ingest.NewServer: %v", err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.Srv, s.Addr = srv, lis.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan error, 1)
	go func() { s.done <- srv.Serve(ctx, lis) }()
	t.Cleanup(func() { _ = s.Stop(t) })
	return s
}

// Stop cancels Serve and returns its result; it fails the test when Serve does
// not return within 10 s (GLI-111: within 5 s plus the forced stop).
func (s *glieServerRig) Stop(t testing.TB) error {
	t.Helper()
	s.once.Do(func() {
		s.cancel()
		select {
		case s.err = <-s.done:
		case <-time.After(10 * time.Second):
			t.Errorf("GLI-111: Serve did not return within 10 s after its context was cancelled")
			s.err = errors.New("Serve did not return")
		}
	})
	return s.err
}

// glieDial opens a mutual-TLS client connection to the ingest server with the
// given node leaf (certificate files read now).
func glieDial(t testing.TB, s *glieServerRig, leaf glieLeaf) *grpc.ClientConn {
	t.Helper()
	cert, err := tlsKeyPair(leaf)
	if err != nil {
		t.Fatalf("load client key pair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(s.PKI.CAPEM)
	conn, err := grpc.NewClient(s.Addr, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs: pool, Certificates: []tls.Certificate{cert}, ServerName: "localhost", MinVersion: tls.VersionTLS13,
	})))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func tlsKeyPair(l glieLeaf) (tls.Certificate, error) { return tls.LoadX509KeyPair(l.Cert, l.Key) }

// glieHello opens a stream with leaf, sends one ClientHello and returns what the
// server answers first: a ServerHello or the stream error (when Send reports io.EOF
// the stream has already ended and the real gRPC status is read with Recv). The stream stays open
// until the returned close function is called.
func glieHello(t testing.TB, s *glieServerRig, leaf glieLeaf, h *ingestpb.ClientHello) (*ingestpb.ServerHello, func(), error) {
	t.Helper()
	conn := glieDial(t, s, leaf)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	stream, err := ingestpb.NewIngestClient(conn).Stream(ctx)
	if err != nil {
		return nil, cancel, err
	}
	if err := stream.Send(&ingestpb.ClientFrame{Frame: &ingestpb.ClientFrame_Hello{Hello: h}}); err != nil {
		if errors.Is(err, io.EOF) { // grpc: the stream already ended; the real status is what Recv returns
			if _, rerr := stream.Recv(); rerr != nil {
				return nil, cancel, rerr
			}
		}
		return nil, cancel, err
	}
	m, err := stream.Recv()
	if err != nil {
		return nil, cancel, err
	}
	return m.GetHello(), cancel, nil
}

func glieClientHello(node *corev1.Node) *ingestpb.ClientHello {
	return &ingestpb.ClientHello{Version: "v1alpha1", ClusterId: glieCluster, NodeName: node.Name, NodeUid: string(node.UID), BootId: glieBootID}
}

// ---------------------------------------------------------------------------
// the S3a controller
// ---------------------------------------------------------------------------

type glieCtlRig struct {
	ID, LeaseID string
	Logs        *glieSyncBuf
	Rec         *glieRecorder
	cancel      context.CancelFunc
	fin         chan struct{}
	once        sync.Once
	err         error
}

// glieStartController runs an in-process S3a controller (ResyncInterval 200 ms,
// Lease 4 s / 3 s / 500 ms in namespace default, GKA-173) with the assessor on a
// recording copy of the envtest config.
func glieStartController(t *testing.T, e *glieEnvT, id, leaseID string, a app.NodeAssessor, clk controller.Clock, mutate func(*controller.Options)) *glieCtlRig {
	t.Helper()
	cfg, rec := glieRecordingConfig(e.Config)
	c := &glieCtlRig{ID: id, LeaseID: leaseID, Logs: &glieSyncBuf{}, Rec: rec, fin: make(chan struct{})}
	opts := controller.Options{
		RESTConfig: cfg, Assessor: a, Clock: clk, ControllerID: id, ClusterID: glieCluster,
		ResyncInterval: 200 * time.Millisecond,
		LeaderElection: controller.LeaderElectionOptions{
			Namespace: glieNS, ID: leaseID, LeaseDuration: 4 * time.Second, RenewDeadline: 3 * time.Second, RetryPeriod: 500 * time.Millisecond,
		},
		Logger: glieLogger(c.Logs),
	}
	if mutate != nil {
		mutate(&opts)
	}
	ctl, err := controller.New(opts)
	if err != nil {
		t.Fatalf("controller.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go func() {
		c.err = ctl.Run(ctx)
		close(c.fin)
	}()
	t.Cleanup(func() { _ = c.Stop(t) })
	return c
}

// Stop cancels the controller and waits for Run to return (GKA-022(a): 10 s).
func (c *glieCtlRig) Stop(t testing.TB) error {
	t.Helper()
	c.once.Do(func() {
		c.cancel()
		select {
		case <-c.fin:
		case <-time.After(15 * time.Second):
			t.Errorf("controller %s: Run did not return within 15 s after the cancellation", c.ID)
		}
	})
	return c.err
}

// glieTee wraps a LiveBundleSource and records the controller's last query and
// the last result, so a test can ask the server's source the same question the
// controller asks (GLI-127 (c): NodeBundles polling observes accepted frames).
type glieTee struct {
	Inner app.LiveBundleSource
	mu    sync.Mutex
	last  app.LiveBundleQuery
	have  bool
	err   error
	devs  int
	calls int
}

func (g *glieTee) NodeBundles(ctx context.Context, q app.LiveBundleQuery) (app.LiveBundleSet, error) {
	set, err := g.Inner.NodeBundles(ctx, q)
	cp := q
	cp.Policy.RequiredCoverage = append([]fleet.CoverageRequirement(nil), q.Policy.RequiredCoverage...)
	cp.Intents = append([]fleet.Intent(nil), q.Intents...)
	g.mu.Lock()
	g.last, g.have, g.err, g.devs = cp, true, err, len(set.Devices)
	g.calls++
	g.mu.Unlock()
	return set, err
}

func (g *glieTee) Last() (app.LiveBundleQuery, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.last, g.have
}

// Result is what the controller's most recent NodeBundles call returned.
func (g *glieTee) Result() (devices int, err error, calls int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.devs, g.err, g.calls
}

// ---------------------------------------------------------------------------
// the agent
// ---------------------------------------------------------------------------

type glieAgentRig struct {
	Dir, Sysfs, SMI, BootFile string
	CAFile, CertFile, KeyFile string
	Leaf                      glieLeaf
	Logs                      *glieSyncBuf
	cancel                    context.CancelFunc
	fin                       chan struct{}
	once                      sync.Once
	err                       error
}

type glieDev struct {
	path          []string
	class, vendor string
}

// glieBuildSysfs writes the GFX-102 BASE topology (GPU under a switch
// downstream under a switch upstream under root port A, a NIC under root port B,
// all widths 16) below root.
func glieBuildSysfs(t testing.TB, root string) {
	t.Helper()
	hb := "pci0000:00"
	for _, d := range []glieDev{
		{[]string{hb, "0000:00:01.0"}, "0x060400", "0x8086"},
		{[]string{hb, "0000:00:01.0", "0000:01:00.0"}, "0x060400", "0x10b5"},
		{[]string{hb, "0000:00:01.0", "0000:01:00.0", "0000:02:08.0"}, "0x060400", "0x10b5"},
		{[]string{hb, "0000:00:01.0", "0000:01:00.0", "0000:02:08.0", glieGPUBDF}, "0x030200", "0x10de"},
		{[]string{hb, "0000:00:02.0"}, "0x060400", "0x8086"},
		{[]string{hb, "0000:00:02.0", "0000:04:00.0"}, "0x020000", "0x15b3"},
	} {
		dir := filepath.Join(root, "devices", filepath.Join(d.path...))
		glieWriteFile(t, filepath.Join(dir, "class"), d.class+"\n", 0o644)
		glieWriteFile(t, filepath.Join(dir, "vendor"), d.vendor+"\n", 0o644)
		glieWriteFile(t, filepath.Join(dir, "current_link_width"), "16\n", 0o644)
		glieWriteFile(t, filepath.Join(dir, "max_link_width"), "16\n", 0o644)
		link := filepath.Join(root, "bus", "pci", "devices", d.path[len(d.path)-1])
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(link), err)
		}
		if err := os.Symlink("../../../devices/"+strings.Join(d.path, "/"), link); err != nil {
			t.Fatalf("symlink %s: %v", link, err)
		}
	}
}

// glieNewAgentFiles prepares the agent's inputs: BASE sysfs, a fake nvidia-smi
// that reports the GPU the device claims, a boot ID file and a node client leaf
// for the Node's UID.
func glieNewAgentFiles(t *testing.T, pki *gliePKI, node *corev1.Node) *glieAgentRig {
	t.Helper()
	a := &glieAgentRig{Dir: t.TempDir(), Logs: &glieSyncBuf{}}
	a.Sysfs = filepath.Join(a.Dir, "sys")
	glieBuildSysfs(t, a.Sysfs)
	a.SMI = filepath.Join(a.Dir, "nvidia-smi")
	glieWriteFile(t, a.SMI, "#!/bin/sh\nprintf '%s, %s\\n' '"+glieGPUUUID+"' '00000000:03:00.0'\n", 0o755)
	a.BootFile = filepath.Join(a.Dir, "boot_id")
	glieWriteFile(t, a.BootFile, glieBootID+"\n", 0o644)
	a.CAFile = filepath.Join(a.Dir, "ca.pem")
	glieWriteFile(t, a.CAFile, string(pki.CAPEM), 0o644)
	a.CertFile = filepath.Join(a.Dir, "client.crt")
	a.KeyFile = filepath.Join(a.Dir, "client.key")
	a.Leaf = pki.Node(t, glieCluster, string(node.UID))
	glieInstall(t, a.CertFile, a.KeyFile, a.Leaf)
	return a
}

// Options are the library options of this agent for the node: Interval 400 ms (so
// that the 5-failed-cycles rule of GLI-083 (3) leaves 2 s for the test driver to
// move the clock) and the shared fake clock.
func (a *glieAgentRig) Options(srv *glieServerRig, node *corev1.Node, clk liveclient.Clock) liveclient.Options {
	_, port, _ := net.SplitHostPort(srv.Addr)
	return liveclient.Options{
		Controller: "localhost:" + port,
		ClusterID:  glieCluster, NodeName: node.Name, NodeUID: string(node.UID),
		SysfsRoot: a.Sysfs, NVIDIASMI: a.SMI, BootIDFile: a.BootFile,
		TLS:      liveclient.TLSFiles{CAFile: a.CAFile, CertFile: a.CertFile, KeyFile: a.KeyFile},
		Interval: 400 * time.Millisecond, HelloTimeout: 5 * time.Second, AckTimeout: 5 * time.Second,
		ReconnectInitial: 200 * time.Millisecond, ReconnectMax: 800 * time.Millisecond,
		Clock: clk, Logger: glieLogger(a.Logs),
	}
}

// Start runs liveclient.Run in a goroutine until Stop or the end of the test.
func (a *glieAgentRig) Start(t *testing.T, o liveclient.Options) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.fin = make(chan struct{})
	go func() {
		a.err = liveclient.Run(ctx, o)
		close(a.fin)
	}()
	t.Cleanup(func() { _ = a.Stop(t) })
}

// Stop cancels the agent and returns Run's result (nil on cancellation).
func (a *glieAgentRig) Stop(t testing.TB) error {
	t.Helper()
	if a.cancel == nil {
		return nil
	}
	a.once.Do(func() {
		a.cancel()
		select {
		case <-a.fin:
		case <-time.After(10 * time.Second):
			t.Errorf("liveclient.Run did not return within 10 s after the cancellation")
		}
	})
	return a.err
}

func (a *glieAgentRig) Running() bool {
	if a.fin == nil {
		return false
	}
	select {
	case <-a.fin:
		return false
	default:
		return true
	}
}

// ---------------------------------------------------------------------------
// the scene and the rig
// ---------------------------------------------------------------------------

type glieScene struct {
	Selector map[string]string
	Node     *corev1.Node
	Fleet    *v1alpha1.GPUFleet
	Device   *v1alpha1.GPUDevice
}

// glieNewScene creates a Node labelled into a new Audit GPUFleet and one
// InService GPUDevice that claims the fake nvidia-smi's GPU. Nothing runs yet.
func glieNewScene(t *testing.T, e *glieEnvT) *glieScene {
	t.Helper()
	sc := &glieScene{Selector: map[string]string{"glie.data-path-assurance.io/fleet": glieName(t, "sel")}}
	sc.Node = glieNode(t, e, glieName(t, "node"), sc.Selector)
	sc.Fleet = glieFleet(t, e, glieName(t, "fleet"), sc.Selector)
	sc.Device = glieDevice(t, e, glieName(t, "dev"), sc.Node, sc.Fleet)
	return sc
}

// glieRig holds everything the end-to-end scenarios use.
type glieRig struct {
	E      *glieEnvT
	Scene  *glieScene
	Clk    *glieClock
	PKI    *gliePKI
	CtlID  string
	Lease  string
	Ad     *glieAdapter
	Srv    *glieServerRig
	Tee    *glieTee
	Ctl    *glieCtlRig
	Agent  *glieAgentRig
	Files  *glieAgentRig
	Frames int
}

func glieNewRig(t *testing.T, e *glieEnvT) *glieRig {
	t.Helper()
	r := &glieRig{E: e, Clk: newGlieClock(glieT0), PKI: glieNewPKI(t), CtlID: glieName(t, "ctl"), Lease: glieName(t, "lease")}
	r.Scene = glieNewScene(t, e)
	r.Files = glieNewAgentFiles(t, r.PKI, r.Scene.Node)
	return r
}

// StartCore follows the production wiring order of GLI-091: the adapter and the
// ingest server (with the system clock replaced by the shared fake clock) are
// created, then the S3a controller is created with controller.New - which
// installs the process-wide klog and controller-runtime loggers once, so no
// client-go activity may already be running (GKA-005) - and only then do the
// controller run and the adapter's leader poll start.
func (r *glieRig) StartCore(t *testing.T, mutateServer func(*ingest.Config)) {
	t.Helper()
	r.Ad = glieNewAdapter(t, r.E, glieAdapterOpts{ControllerID: r.CtlID, LeaseName: r.Lease, NoPoll: true})
	srvCfg := func(c *ingest.Config) {
		c.Clock = r.Clk
		if mutateServer != nil {
			mutateServer(c)
		}
	}
	r.Srv = glieStartServer(t, r.Ad, r.PKI, srvCfg)
	r.Tee = &glieTee{Inner: r.Srv.Srv.BundleSource()}
	r.Ctl = glieStartController(t, r.E, r.CtlID, r.Lease, app.NewLiveAssessor(r.Tee), r.Clk, nil)
	r.Ad.StartPoll(t) // after controller.New (GLI-091 (4) before (6))
	r.Ad.WaitLeading(t, true, 20*time.Second)
}

// StartAgent runs the library client against the server with the shared clock.
func (r *glieRig) StartAgent(t *testing.T, mutate func(*liveclient.Options)) {
	t.Helper()
	r.Agent = r.Files
	o := r.Files.Options(r.Srv, r.Scene.Node, r.Clk)
	if mutate != nil {
		mutate(&o)
	}
	r.Files.Start(t, o)
}

// NPS returns the node's NodePathState (nil when it does not exist).
func (r *glieRig) NPS() *v1alpha1.NodePathState {
	n, _ := glieGetNPS(r.E, r.Scene.Node.Name)
	return n
}

// Device returns the GPUDevice as stored.
func (r *glieRig) Device(t testing.TB) *v1alpha1.GPUDevice {
	t.Helper()
	d := &v1alpha1.GPUDevice{}
	if err := r.E.Client.Get(context.Background(), client.ObjectKey{Name: r.Scene.Device.Name}, d); err != nil {
		t.Fatalf("get GPUDevice %s: %v", r.Scene.Device.Name, err)
	}
	return d
}

// Published returns the (session, sequence) of the graphRevision S3a published
// for the node, and whether there is one.
func (r *glieRig) Published() (session, sequence int64, ok bool) {
	n := r.NPS()
	if n == nil || n.Status.GraphRevision == nil {
		return 0, 0, false
	}
	return glieRevision(*n.Status.GraphRevision)
}

// Drive moves the shared fake clock by 15 s every time S3a has published a frame
// it has not seen yet (a new graphRevision) - so the agent's next cycle carries a
// later time (GLI-127 (a)) and the rate bucket refills - until done reports true
// or the timeout expires.
func (r *glieRig) Drive(t *testing.T, timeout time.Duration, done func() (bool, string)) {
	t.Helper()
	var lastSession, lastSeq int64 = -1, -1
	glieEventually(t, timeout, func() (bool, string) {
		if s, q, ok := r.Published(); ok && (s != lastSession || q != lastSeq) {
			lastSession, lastSeq = s, q
			r.Clk.Advance(15 * time.Second)
			r.Frames++
		}
		return done()
	})
}

// Probe asks the server's bundle source the question the controller asked last
// (same policy, intents and node) at the current fake time.
func (r *glieRig) Probe(t testing.TB) (devices int, err error) {
	t.Helper()
	q, ok := r.Tee.Last()
	if !ok {
		return 0, errors.New("the controller has not queried the source yet")
	}
	q.Now = r.Clk.Now()
	set, err := r.Srv.Srv.BundleSource().NodeBundles(context.Background(), q)
	return len(set.Devices), err
}

// newNoObservationAssessor is the S3a product wiring without ingest:
// NewLiveAssessor(NewNoObservationSource()).
func newNoObservationAssessor() app.NodeAssessor {
	return app.NewLiveAssessor(app.NewNoObservationSource())
}

// DriveBackground runs the clock driver of Drive in the background until stop is
// called (or the test ends): the fake clock moves by 15 s for every newly
// published frame, so a scenario that does something else meanwhile keeps the
// agent's cycles later than the previous frame (GLI-127 (a)).
func (r *glieRig) DriveBackground(t *testing.T) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		var lastSession, lastSeq int64 = -1, -1
		for ctx.Err() == nil {
			if s, q, ok := r.Published(); ok && (s != lastSession || q != lastSeq) {
				lastSession, lastSeq = s, q
				r.Clk.Advance(15 * time.Second)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
	t.Cleanup(stop)
	return stop
}
