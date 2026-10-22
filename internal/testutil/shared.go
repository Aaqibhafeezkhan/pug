package testutil

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

// cleanupDropTimeout bounds the per-test cleanup that drops a database or flushes
// a Redis index once a test finishes. Those run on context.Background (t.Context
// is already cancelled by cleanup time), which without a deadline would let a
// wedged connection block the drop — and with it the rest of the sequentially run
// package — until go test -timeout fired with a generic goroutine dump. This is
// the backstop against that hang: deliberately far longer than any real drop (a
// ClickHouse sync drop under a saturated daemon must have room to finish, or it
// fails and leaves its data behind) and far shorter than the suite timeout.
const cleanupDropTimeout = 30 * time.Second

// Main runs a package's tests and then releases the containers they shared.
//
// Every package whose tests call a Setup* helper must wire this up:
//
//	func TestMain(m *testing.M) { testutil.Main(m) }
//
// Containers outlive the tests that start them, so Ryuk reaps them, even after
// a crash. The teardown closes the package's pools and drops its ClickHouse
// template, killing the containers only when Ryuk is off.
// TestSetupCallersWireMain enforces the wiring.
func Main(m *testing.M) {
	forceRyuk()
	extendRyukGrace()

	code := m.Run()

	if err := teardowns.run(); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: shutdown: %v\n", err)
		if code == 0 {
			code = 1
		}
	}

	os.Exit(code)
}

// forceRyuk turns Ryuk on over a ~/.testcontainers.properties that disables it,
// since release leaves every container to Ryuk. An explicit environment opt-out
// still wins, and this must run before testcontainers first reads its config.
func forceRyuk() {
	if _, explicit := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); explicit {
		return
	}
	if err := os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "false"); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: enable ryuk: %v\n", err)
	}
}

// ryukGrace is how long Ryuk waits once no package holds a container before it
// reaps them all, mid-run included. Building and running packages without
// containers can open a long gap; waiting longer only delays cleanup.
const ryukGrace = "3m"

// extendRyukGrace sets ryukGrace unless the environment already sets a timeout.
// It sets the unprefixed name: testcontainers marks the TESTCONTAINERS_ one for
// removal.
func extendRyukGrace() {
	for _, env := range []string{"RYUK_RECONNECTION_TIMEOUT", "TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT"} {
		if os.Getenv(env) != "" {
			return
		}
	}
	if err := os.Setenv("RYUK_RECONNECTION_TIMEOUT", ryukGrace); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: extend ryuk grace: %v\n", err)
	}
}

// ryukReaps reports whether Ryuk reaps this session's containers, as
// testcontainers resolves it from the environment and its properties file.
func ryukReaps() bool {
	return !testcontainers.ReadConfig().Config.RyukDisabled
}

// runScopedName names a container every package of this go test run shares, or
// returns "" when Ryuk is off: only Ryuk reaps a container that outlives the
// package that started it, so without it each package keeps its own.
func runScopedName(kind string) string {
	if !ryukReaps() {
		return ""
	}
	id := testcontainers.SessionID()
	return "pug-test-" + kind + "-" + id[:min(12, len(id))]
}

// release ends a package's use of a container. With Ryuk on that is nothing: a
// run-scoped one may still serve other packages, and Ryuk reaps them all without
// the seconds a stop costs here. Without Ryuk it is a kill.
func release(ctr testcontainers.Container) error {
	if ryukReaps() {
		return nil
	}
	return testcontainers.TerminateContainer(ctr, testcontainers.StopTimeout(0))
}

// lazyContainer holds a container started on first use and shared by the rest
// of the package's tests.
type lazyContainer[T any] struct {
	kind  string
	start func() (*T, error)

	mu  sync.Mutex
	val *T
}

// get returns the shared container, starting it if this is the first call.
//
// A failed start is deliberately not memoized. Container starts time out when
// the Docker daemon is saturated — which is exactly when a whole-suite run is
// underway — and that is transient: the next test may well get its container.
// Caching the error would turn one unlucky start into a failure for every
// remaining test in the package, each reported against a stale message that
// points at the daemon rather than at the test that actually tripped.
func (l *lazyContainer[T]) get(t *testing.T) *T {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.val != nil {
		return l.val
	}
	val, err := l.start()
	if err != nil {
		t.Fatalf("testutil: start shared %s: %v", l.kind, err)
	}
	l.val = val
	return l.val
}

// teardownRegistry collects what a package's shared containers need done when its
// tests finish. The zero value is ready to use.
type teardownRegistry struct {
	mu  sync.Mutex
	fns []func() error
}

// teardowns is the registry Main drains. It is package state because Go's testing
// framework offers nowhere else to put it: TestMain cannot hand a value to the
// tests that register against it, so the one process-wide instance is the only
// thing both ends can reach. The type keeps that reachability from also meaning
// the mutex and the slice are separately addressable — every caller goes through
// a method that holds the right lock.
var teardowns teardownRegistry

// add records a teardown to run once the package's tests finish.
func (r *teardownRegistry) add(fn func() error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fns = append(r.fns, fn)
}

// run executes every teardown, most recently registered first, and reports what
// failed. It drains the registry, so a second call is a no-op.
//
// The teardowns run off the lock: each one stops a container, and holding a
// non-reentrant mutex across that would deadlock a teardown that registered
// another. Re-draining until the registry is empty means such a late
// registration runs too, rather than being stranded when Main exits.
func (r *teardownRegistry) run() error {
	var errs []error
	for {
		r.mu.Lock()
		fns := r.fns
		r.fns = nil
		r.mu.Unlock()

		if len(fns) == 0 {
			break
		}
		for _, fn := range slices.Backward(fns) {
			errs = append(errs, runTeardown(fn))
		}
	}
	return errors.Join(errs...)
}

// runTeardown contains a panicking teardown, which would otherwise strand every
// container still queued behind it and escape Main before it could exit with the
// suite's own status — reporting a green run as a crash, under a stack through
// teardown plumbing.
func runTeardown(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("teardown panicked: %v", r)
		}
	}()
	return fn()
}
