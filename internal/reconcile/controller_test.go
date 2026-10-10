package reconcile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/hook"
	"github.com/lwmacct/260907-pemcast/internal/state"
)

type fetchCall struct {
	target     string
	generation string
	revision   int64
}

type fakeSource struct {
	mu       sync.Mutex
	calls    []fetchCall
	material *bundle.Material
	err      error
	delay    time.Duration
	active   atomic.Int64
	max      atomic.Int64
}

func (s *fakeSource) FetchBundle(_ context.Context, target, generation string, revision int64) (*bundle.Material, error) {
	call := fetchCall{target: target, generation: generation, revision: revision}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()

	if s.delay > 0 {
		timer := time.NewTimer(s.delay)
		defer timer.Stop()
		<-timer.C
	}
	active := s.active.Add(1)
	for {
		observed := s.max.Load()
		if active <= observed || s.max.CompareAndSwap(observed, active) {
			break
		}
	}
	defer s.active.Add(-1)

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.material, s.err
}

func (s *fakeSource) recorded() []fetchCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fetchCall(nil), s.calls...)
}

type fakeDeployer struct {
	mu           sync.Mutex
	calls        []string
	changedCalls int
	result       deploy.Result
	err          error
}

func (d *fakeDeployer) Activate(_ *bundle.Material, _ config.Output) (deploy.Result, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, d.callsName())
	if d.result.Changed {
		d.changedCalls++
	}
	return d.result, d.err
}

func (d *fakeDeployer) callsName() string {
	if len(d.calls) == 0 {
		return "first"
	}
	return fmt.Sprintf("call-%d", len(d.calls)+1)
}

func (d *fakeDeployer) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *fakeDeployer) changedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.changedCalls
}

type fakeHookRunner struct {
	mu    sync.Mutex
	calls []hook.Event
	errs  []error
}

func (r *fakeHookRunner) Run(_ context.Context, _ config.Hook, event hook.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, event)
	var err error
	if len(r.errs) > 0 {
		err = r.errs[0]
		r.errs = r.errs[1:]
	}
	return err
}

func (r *fakeHookRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fakeHookRunner) events() []hook.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hook.Event(nil), r.calls...)
}

type fakeStateStore struct {
	mu        sync.Mutex
	targets   map[string]state.Target
	loadErr   error
	saveErr   error
	loadCalls int
	saveCalls int
}

func newFakeState(targets map[string]state.Target) *fakeStateStore {
	return &fakeStateStore{targets: targets}
}

func (s *fakeStateStore) Load(targetID string) (state.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadCalls++
	if s.loadErr != nil {
		return state.Target{}, s.loadErr
	}
	return s.targets[targetID], nil
}

func (s *fakeStateStore) Save(targetID string, target state.Target) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	if s.targets == nil {
		s.targets = make(map[string]state.Target)
	}
	s.targets[targetID] = target
	return nil
}

func (s *fakeStateStore) target(id string) state.Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targets[id]
}

func (s *fakeStateStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadCalls, s.saveCalls
}

func testCertificatePEM(t *testing.T) ([]byte, []byte) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pemcast-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certificate, key
}

func testMaterial(t *testing.T, digest string) *bundle.Material {
	t.Helper()

	certificate, privateKey := testCertificatePEM(t)
	manifest, _ := bundle.NewTLSManifest(bundle.TypeTLSServer, certificate, privateKey)
	encoded, err := bundle.Encode(manifest)
	require.NoError(t, err)
	_, files, _, err := bundle.Decode(encoded)
	require.NoError(t, err)
	certificates, leaf, err := manifest.ValidateFiles(files)
	require.NoError(t, err)
	return &bundle.Material{
		TargetID: "nginx", Generation: "generation-1", Revision: 42,
		Manifest: manifest, Files: files, Digest: digest,
		Certificates: certificates, Leaf: leaf,
	}
}

func testAgentConfig(targets ...config.Target) config.Agent {
	if len(targets) == 0 {
		targets = []config.Target{testTarget("nginx")}
	}
	return config.Agent{MaxConcurrent: 4, Targets: targets}
}

func testTarget(id string) config.Target {
	return config.Target{
		ID:           id,
		Type:         bundle.TypeTLSServer,
		DeletePolicy: "retain",
		Output: config.Output{
			Root:        "/tmp/pemcast/" + id,
			CurrentLink: "current",
			Mappings: []config.FileMapping{
				{Remote: bundle.NameCertificateChain, Local: "fullchain.pem"},
				{Remote: bundle.NamePrivateKey, Local: "privkey.pem"},
			},
		},
	}
}

func newTestController(source Source, deployer Deployer, hooks HookRunner, store StateStore, cfg config.Agent) *Controller {
	return New(source, deployer, hooks, store, cfg, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
}

func TestReconcileFetchFailureDoesNotTouchLocalState(t *testing.T) {
	source := &fakeSource{err: errors.New("remote unavailable")}
	deployer := &fakeDeployer{}
	hooks := &fakeHookRunner{}
	store := newFakeState(nil)
	controller := newTestController(source, deployer, hooks, store, testAgentConfig())

	err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42)
	if err == nil || !errors.Is(err, source.err) {
		t.Fatalf("Reconcile() error = %v, want %v", err, source.err)
	}
	if deployer.count() != 0 || hooks.count() != 0 {
		t.Fatalf("deploy/hooks ran after fetch failure: deploy=%d hooks=%d", deployer.count(), hooks.count())
	}
	if loads, saves := store.counts(); loads != 0 || saves != 0 {
		t.Fatalf("state touched after fetch failure: loads=%d saves=%d", loads, saves)
	}
}

func TestReconcileValidationFailureStopsBeforeState(t *testing.T) {
	material := testMaterial(t, "digest")
	material.Manifest.Type = bundle.TypeTrust
	source := &fakeSource{material: material}
	deployer := &fakeDeployer{}
	hooks := &fakeHookRunner{}
	store := newFakeState(nil)
	controller := newTestController(source, deployer, hooks, store, testAgentConfig())

	err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42)
	if err == nil {
		t.Fatal("Reconcile() succeeded with mismatched bundle type")
	}
	if deployer.count() != 0 || hooks.count() != 0 {
		t.Fatalf("deploy/hooks ran after validation failure: deploy=%d hooks=%d", deployer.count(), hooks.count())
	}
	if loads, saves := store.counts(); loads != 0 || saves != 0 {
		t.Fatalf("state touched after validation failure: loads=%d saves=%d", loads, saves)
	}
}

func TestReconcileDryRunDoesNotWrite(t *testing.T) {
	source := &fakeSource{material: testMaterial(t, "digest")}
	deployer := &fakeDeployer{}
	hooks := &fakeHookRunner{}
	store := newFakeState(nil)
	cfg := testAgentConfig()
	cfg.DryRun = true
	controller := newTestController(source, deployer, hooks, store, cfg)

	if err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if deployer.count() != 0 || hooks.count() != 0 {
		t.Fatalf("dry-run deployed or ran hook: deploy=%d hooks=%d", deployer.count(), hooks.count())
	}
	if loads, saves := store.counts(); loads != 0 || saves != 0 {
		t.Fatalf("dry-run touched state: loads=%d saves=%d", loads, saves)
	}
	if len(store.targets) != 0 {
		t.Fatalf("dry-run state was modified: %#v", store.targets)
	}
}

func TestReconcileUpdatesMetadataForUnchangedDigest(t *testing.T) {
	activatedAt := time.Now().Add(-time.Minute).UTC()
	source := &fakeSource{material: testMaterial(t, "digest")}
	deployer := &fakeDeployer{result: deploy.Result{CurrentDir: "/tmp/pemcast/nginx/current"}}
	hooks := &fakeHookRunner{}
	store := newFakeState(map[string]state.Target{
		"nginx": {Generation: "old", Digest: "old-digest", Revision: 20, ActivatedAt: activatedAt},
	})
	controller := newTestController(source, deployer, hooks, store, testAgentConfig())

	if err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if deployer.count() != 1 || hooks.count() != 0 {
		t.Fatalf("unexpected deploy/hook counts: deploy=%d hooks=%d", deployer.count(), hooks.count())
	}
	got := store.target("nginx")
	if got.Generation != "generation-1" || got.Digest != "digest" || got.Revision != 42 {
		t.Fatalf("unchanged release metadata was not updated: %#v", got)
	}
	if !got.ActivatedAt.Equal(activatedAt) {
		t.Fatalf("ActivatedAt changed for unchanged release: got %v want %v", got.ActivatedAt, activatedAt)
	}
}

func TestReconcileChangedDigestRunsHookAndSavesState(t *testing.T) {
	source := &fakeSource{material: testMaterial(t, "digest")}
	deployer := &fakeDeployer{result: deploy.Result{
		Changed: true, ReleaseDir: "/tmp/pemcast/nginx/.pemcast/releases/digest", CurrentDir: "/tmp/pemcast/nginx/current",
	}}
	hooks := &fakeHookRunner{}
	store := newFakeState(nil)
	controller := newTestController(source, deployer, hooks, store, testAgentConfig())

	if err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	events := hooks.events()
	if len(events) != 1 {
		t.Fatalf("hook call count = %d, want 1", len(events))
	}
	event := events[0]
	if event.TargetID != "nginx" || event.Generation != "generation-1" || event.PreviousGeneration != "" {
		t.Fatalf("unexpected hook event: %#v", event)
	}
	if event.ChangedFiles[0] != "fullchain.pem" || event.ChangedFiles[1] != "privkey.pem" {
		t.Fatalf("unexpected changed files: %#v", event.ChangedFiles)
	}
	got := store.target("nginx")
	if got.Generation != "generation-1" || got.Digest != "digest" || got.Revision != 42 || got.HookError != "" {
		t.Fatalf("unexpected saved state: %#v", got)
	}
}

func TestReconcileRecordsHookFailureAndRetriesWithoutRewritingRelease(t *testing.T) {
	source := &fakeSource{material: testMaterial(t, "digest")}
	current := deploy.Result{CurrentDir: "/tmp/pemcast/nginx/current"}
	deployer := &fakeDeployer{result: deploy.Result{
		Changed: true, ReleaseDir: "/tmp/pemcast/nginx/.pemcast/releases/digest", CurrentDir: current.CurrentDir,
	}}
	hookFailure := errors.New("reload failed")
	hooks := &fakeHookRunner{errs: []error{hookFailure, nil}}
	store := newFakeState(nil)
	controller := newTestController(source, deployer, hooks, store, testAgentConfig())

	err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42)
	if err == nil || !errors.Is(err, hookFailure) {
		t.Fatalf("first Reconcile() error = %v, want %v", err, hookFailure)
	}
	failed := store.target("nginx")
	if failed.HookError == "" || failed.Generation != "generation-1" {
		t.Fatalf("hook failure was not recorded: %#v", failed)
	}

	deployer.result = current
	if err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42); err != nil {
		t.Fatalf("retry Reconcile() error = %v", err)
	}
	if deployer.changedCount() != 1 {
		t.Fatalf("retry rewrote release: changed activations=%d", deployer.changedCount())
	}
	if hooks.count() != 2 {
		t.Fatalf("hook calls after retry=%d, want 2", hooks.count())
	}
	if got := store.target("nginx"); got.HookError != "" {
		t.Fatalf("hook error was not cleared: %#v", got)
	}
}

func TestReconcileReturnsStateSaveFailure(t *testing.T) {
	source := &fakeSource{material: testMaterial(t, "digest")}
	deployer := &fakeDeployer{result: deploy.Result{Changed: true, CurrentDir: "/tmp/pemcast/nginx/current"}}
	hooks := &fakeHookRunner{}
	saveErr := errors.New("disk full")
	store := &fakeStateStore{saveErr: saveErr}
	controller := newTestController(source, deployer, hooks, store, testAgentConfig())

	err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42)
	if err == nil || !errors.Is(err, saveErr) {
		t.Fatalf("Reconcile() error = %v, want %v", err, saveErr)
	}
	if hooks.count() != 1 {
		t.Fatalf("hook did not run before state save: calls=%d", hooks.count())
	}
}

func TestReconcileSnapshotDeletePolicies(t *testing.T) {
	cfg := testAgentConfig(testTarget("retain"))
	cfg.Targets[0].DeletePolicy = "retain"
	controller := newTestController(&fakeSource{}, &fakeDeployer{}, &fakeHookRunner{}, newFakeState(nil), cfg)
	if err := controller.ReconcileSnapshot(t.Context(), etcdsource.Snapshot{Active: map[string]string{}}); err != nil {
		t.Fatalf("retain policy returned error: %v", err)
	}

	cfg = testAgentConfig(testTarget("fail"))
	cfg.Targets[0].DeletePolicy = "fail"
	controller = newTestController(&fakeSource{}, &fakeDeployer{}, &fakeHookRunner{}, newFakeState(nil), cfg)
	err := controller.ReconcileSnapshot(t.Context(), etcdsource.Snapshot{Active: map[string]string{}})
	if err == nil || !strings.Contains(err.Error(), "active pointer was deleted") {
		t.Fatalf("fail policy error = %v, want deletion error", err)
	}
}

func TestReconcileSerializesSameTarget(t *testing.T) {
	source := &fakeSource{material: testMaterial(t, "digest"), delay: 10 * time.Millisecond}
	deployer := &fakeDeployer{result: deploy.Result{CurrentDir: "/tmp/pemcast/nginx/current"}}
	store := newFakeState(nil)
	controller := newTestController(source, deployer, &fakeHookRunner{}, store, testAgentConfig())

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := controller.Reconcile(t.Context(), "nginx", "generation-1", 42); err != nil {
				t.Errorf("Reconcile() error = %v", err)
			}
		})
	}
	wg.Wait()

	if got := source.max.Load(); got != 1 {
		t.Fatalf("maximum concurrent fetches for one target=%d, want 1", got)
	}
	if len(source.recorded()) != 4 {
		t.Fatalf("fetch count=%d, want 4", len(source.recorded()))
	}
}

func TestReconcileSnapshotLimitsGlobalConcurrency(t *testing.T) {
	source := &fakeSource{material: testMaterial(t, "digest"), delay: 10 * time.Millisecond}
	deployer := &fakeDeployer{result: deploy.Result{CurrentDir: "/tmp/pemcast/nginx/current"}}
	cfg := testAgentConfig(testTarget("one"), testTarget("two"), testTarget("three"))
	cfg.MaxConcurrent = 1
	controller := newTestController(source, deployer, &fakeHookRunner{}, newFakeState(nil), cfg)

	err := controller.ReconcileSnapshot(t.Context(), etcdsource.Snapshot{
		Active: map[string]string{"one": "g", "two": "g", "three": "g"},
	})
	if err != nil {
		t.Fatalf("ReconcileSnapshot() error = %v", err)
	}
	if got := source.max.Load(); got != 1 {
		t.Fatalf("maximum concurrent fetches=%d, want 1", got)
	}
}
