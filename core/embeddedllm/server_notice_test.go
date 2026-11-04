// The load-time free-memory pre-flight: the fresh measurement the launch
// already paid for is also compared with the footprint the plan projects, and
// a shortfall becomes a Warn plus a NoticeLowFreeDeviceMemory — never a gate.
// These tests pin the three properties the feature exists for:
//
//  1. A fresh free < projection produces exactly one Warn (intercepted at the
//     source, through the test-injected logger) and exactly one notice, whose
//     fields name the evidence.
//  2. Occupied VRAM does NOT change the plan: the shape that launches with a
//     nearly-full accelerator is byte-for-byte the shape that launches with an
//     empty one, because budgets price TOTAL capacity (the Total-budgeting
//     invariant — see plan.go's buildTopology).
//  3. The check keeps the probe's fail-soft contract: a probe that did not
//     answer leaves the recorded snapshot in place, and a stale snapshot never
//     earns a warning — there is no honest "free" figure to warn about.
package embeddedllm

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// noticeRecorder collects the ServerNotice values the supervisor delivers
// through Server.OnNotice. The pre-flight runs synchronously inside Load, so
// the lock is belt-and-braces for future async emitters, not for this one.
type noticeRecorder struct {
	mu      sync.Mutex
	notices []ServerNotice
}

func (r *noticeRecorder) onNotice(n ServerNotice) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, n)
}

func (r *noticeRecorder) all() []ServerNotice {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ServerNotice, len(r.notices))
	copy(out, r.notices)
	return out
}

// lowFreeCUDATopology is the fresh answer of a machine whose card is mostly
// occupied: same shape as the topology TestLoadReplansFromAFreshTopology
// replans successfully from (so the refinement path genuinely runs), but
// `free` MiB of the 24576 MiB total are held by something else. ProbedAt is
// advanced so the fresh measurement is distinct from the recorded snapshot.
func lowFreeCUDATopology(free int64) MemoryTopology {
	return MemoryTopology{
		Devices: []DeviceMemory{
			{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24576, FreeMiB: free},
		},
		HostRAMGiB:        128,
		Unified:           false,
		DeviceBudgetBytes: 24576 * bytesPerMiB,
		HostBudgetBytes:   112 * 1024 * bytesPerMiB,
		ProbedAt:          testInstallInstant.Add(time.Hour).Format(time.RFC3339),
	}
}

// TestLoadWarnsWhenFreeMemoryIsBelowTheProjection is the acceptance case: a
// fresh probe that finds less free memory than the plan projects warns at the
// source and delivers one structured notice, while the load itself proceeds
// on the unchanged shape.
func TestLoadWarnsWhenFreeMemoryIsBelowTheProjection(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{
		backend: BackendCUDA124,
		probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return lowFreeCUDATopology(8000), true
		},
	})
	notices := &noticeRecorder{}
	fx.srv.OnNotice = notices.onNotice

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load with free memory below the projection: %v", err)
	}
	waitForState(t, fx.srv, StateLoaded)

	// The evidence must agree with the shape the disk now records: the
	// projection the launch applied, priced from TOTAL capacity. Read first,
	// so every assertion below names the same figure the planner chose.
	onDisk := fx.diskManifest(t)
	if onDisk.Plan == nil {
		t.Fatal("the manifest carries no plan after the load")
	}
	expected := onDisk.Plan.ExpectedDeviceMiB
	if expected <= 8000 {
		t.Fatalf("the replanned projection %d leaves no shortfall to warn about", expected)
	}

	// ONE Warn, at the source, naming the evidence. The fixture's own
	// logCapture IS the interception point — the subsystem logger the
	// supervisor was built with.
	logged := fx.logs.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Fatalf("no Warn was logged for the free-memory shortfall:\n%s", logged)
	}
	if !strings.Contains(logged, fmt.Sprintf("the accelerator has 8000 MiB free, below the %d MiB", expected)) {
		t.Errorf("the Warn does not carry the measured free and projected figures:\n%s", logged)
	}
	if strings.Count(logged, "the accelerator has 8000 MiB free") != 1 {
		t.Errorf("the free-memory Warn fired more than once:\n%s", logged)
	}

	// ONE notice, carrying the same evidence as structured fields.
	delivered := notices.all()
	if len(delivered) != 1 {
		t.Fatalf("OnNotice delivered %d notices, want 1: %+v", len(delivered), delivered)
	}
	notice := delivered[0]
	if notice.Kind != NoticeLowFreeDeviceMemory {
		t.Errorf("notice kind = %q, want %q", notice.Kind, NoticeLowFreeDeviceMemory)
	}
	if notice.FreeDeviceMiB != 8000 {
		t.Errorf("notice free_device_mib = %d, want 8000", notice.FreeDeviceMiB)
	}
	if notice.ExpectedDeviceMiB != expected {
		t.Errorf("notice expected_device_mib = %d, want the applied plan's %d", notice.ExpectedDeviceMiB, expected)
	}
	if notice.Message == "" || !strings.Contains(notice.Message, "8000") {
		t.Errorf("notice message is not the renderable sentence: %q", notice.Message)
	}
}

// TestOccupiedVRAMDoesNotChangeThePlan is the Total-budgeting invariant at the
// pre-flight's seam: with the SAME total capacity, a nearly-full accelerator
// and an empty one launch the identical shape. The pre-flight only reports.
func TestOccupiedVRAMDoesNotChangeThePlan(t *testing.T) {
	t.Parallel()

	type launch struct {
		args []string
		plan *MemoryPlan
	}
	run := func(t *testing.T, free int64) launch {
		t.Helper()
		fx := memoryAwareFixture(t, fixtureOptions{
			backend: BackendCUDA124,
			probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
				return lowFreeCUDATopology(free), true
			},
		})
		if err := fx.srv.Load(t.Context()); err != nil {
			t.Fatalf("Load (free = %d MiB): %v", free, err)
		}
		onDisk := fx.diskManifest(t)
		return launch{args: fx.spawner.lastCommand().Args, plan: onDisk.Plan}
	}

	loaded := run(t, 8000)
	idle := run(t, 24576)

	// The plan bytes are identical: free memory is not a plan input.
	if !reflect.DeepEqual(loaded.plan, idle.plan) {
		t.Errorf("the applied plan moved with occupancy:\nloaded: %+v\nidle:   %+v", loaded.plan, idle.plan)
	}
	// So is the whole command line. The port is the one legitimate
	// difference (each fixture binds its own endpoint), so it is blanked
	// before the comparison rather than picking plan flags by hand — a list
	// that would have to know whether this plan is fit-sized.
	if !slices.Equal(blankPort(loaded.args), blankPort(idle.args)) {
		t.Errorf("the command line moved with occupancy:\nloaded: %v\nidle:   %v", loaded.args, idle.args)
	}
}

// blankPort blanks every argv value that legitimately differs between two
// fixture runs — the port (each fixture binds its own endpoint) and the model
// / projector paths (each fixture stages its own tree) — so two launches
// compare equal everywhere the launch SHAPE is concerned.
func blankPort(args []string) []string {
	out := slices.Clone(args)
	for i, arg := range out {
		if (arg == "--port" || arg == "-m" || arg == "--mmproj") && i+1 < len(out) {
			out[i+1] = "<" + arg + ">"
		}
	}
	return out
}

// TestNoNoticeWhenTheProbeDoesNotAnswer keeps the pre-flight inside the
// probe's fail-soft contract: when the driver query wedges, the recorded
// snapshot carries the launch, and a snapshot's free figures describe install
// day — warning about them would be noise anchored to nothing current.
func TestNoNoticeWhenTheProbeDoesNotAnswer(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{
		probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return MemoryTopology{}, false
		},
	})
	fx.srv.LoadProbeTimeout = 100 * time.Millisecond
	notices := &noticeRecorder{}
	fx.srv.OnNotice = notices.onNotice

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load with a wedged probe: %v", err)
	}
	if delivered := notices.all(); len(delivered) != 0 {
		t.Errorf("a stale snapshot produced %d notices, want 0: %+v", len(delivered), delivered)
	}
	if strings.Contains(fx.logs.String(), "the accelerator has") {
		t.Errorf("a stale snapshot earned a free-memory Warn:\n%s", fx.logs.String())
	}
}

// TestNoNoticeWhenFreeMemoryCoversTheProjection pins the quiet half: a
// machine whose free memory covers the projection stays silent — the
// pre-flight is a shortfall finding, not a load report. The staged CUDA plan
// is shrunk on disk first, so the replan (identical inputs except free)
// applies a projection the 8000 free MiB do cover... which the pure planner
// would refuse to call a projection at all, so the plan is checked the
// pre-flight's own way: with the manifest's recorded plan NOT refreshed
// (identical replan), the shrink must land in the applied plan to be
// meaningful. The direct pre-flight unit below carries that case instead.
func TestNoNoticeWhenFreeMemoryCoversTheProjection(t *testing.T) {
	t.Parallel()

	// The direct unit: the seam-level check with a covered projection is
	// quiet, whatever the plan came from.
	srv := NewServer(Layout{}, slog.New(slog.DiscardHandler))
	notices := &noticeRecorder{}
	srv.OnNotice = notices.onNotice
	topology := lowFreeCUDATopology(8000)
	plan := stagedPlan()
	plan.ExpectedDeviceMiB = 4000
	srv.preflightFreeMemory(&plan, &topology)
	if delivered := notices.all(); len(delivered) != 0 {
		t.Errorf("a covered projection produced %d notices, want 0: %+v", len(delivered), delivered)
	}

	// The guards: no topology, no devices, no plan, a non-positive projection.
	for name, tc := range map[string]struct {
		plan     *MemoryPlan
		topology *MemoryTopology
	}{
		"nil plan":     {nil, &topology},
		"nil topology": {&plan, nil},
		"no devices":   {&plan, func() *MemoryTopology { cp := topology; cp.Devices = nil; return &cp }()},
		"zero projection": {func() *MemoryPlan {
			cp := plan
			cp.ExpectedDeviceMiB = 0
			return &cp
		}(), &topology},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := &noticeRecorder{}
			s := NewServer(Layout{}, slog.New(slog.DiscardHandler))
			s.OnNotice = rec.onNotice
			s.preflightFreeMemory(tc.plan, tc.topology)
			if delivered := rec.all(); len(delivered) != 0 {
				t.Errorf("guard %s delivered %d notices, want 0", name, len(delivered))
			}
		})
	}
}

// TestNoNoticeWithoutTheSeam: OnNotice nil (the pre-existing wiring shape)
// means the finding stays a Warn in the log — nothing may panic or block.
func TestNoNoticeWithoutTheSeam(t *testing.T) {
	t.Parallel()

	fx := memoryAwareFixture(t, fixtureOptions{
		backend: BackendCUDA124,
		probeDevices: func(context.Context, string, *slog.Logger) (MemoryTopology, bool) {
			return lowFreeCUDATopology(8000), true
		},
	})
	// fx.srv.OnNotice deliberately left nil.

	if err := fx.srv.Load(t.Context()); err != nil {
		t.Fatalf("Load without OnNotice: %v", err)
	}
	if !strings.Contains(fx.logs.String(), "the accelerator has 8000 MiB free") {
		t.Errorf("the Warn did not survive a nil OnNotice:\n%s", fx.logs.String())
	}
}

// flagValue's shared definition lives in server_test.go.

// TestFreePoolMiBMirrorsDevicePoolMiB pins the pure helper's two readings and
// the pin narrowing: unified pools take the largest single entry's free (the
// entries alias one physical pool), discrete pools sum (the runtime
// tensor-splits across them), and a `-dev` pin restricts the reading to
// exactly the named devices.
func TestFreePoolMiBMirrorsDevicePoolMiB(t *testing.T) {
	t.Parallel()

	discrete := []DeviceMemory{
		{Name: "CUDA0", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24576, FreeMiB: 20000},
		{Name: "CUDA1", Description: "NVIDIA GeForce RTX 4090", TotalMiB: 24576, FreeMiB: 1000},
	}
	unified := []DeviceMemory{
		{Name: "MTL0", Description: "Apple M3 Pro", TotalMiB: 24576, FreeMiB: 8000},
		{Name: "MTL1", Description: "Apple M3 Pro", TotalMiB: 24576, FreeMiB: 24000},
	}

	tests := []struct {
		name    string
		devices []DeviceMemory
		unified bool
		pinned  []string
		want    int64
	}{
		{"empty inventory", nil, false, nil, 0},
		{"discrete sums the free pools", discrete, false, nil, 21000},
		{"unified takes the largest free", unified, true, nil, 24000},
		{"discrete pin narrows the sum", discrete, false, []string{"CUDA1"}, 1000},
		{"unified pin narrows to the alias", unified, true, []string{"MTL0"}, 8000},
		{"unknown pinned names measure nothing", discrete, false, []string{"CUDA9"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := freePoolMiB(tc.devices, tc.unified, tc.pinned); got != tc.want {
				t.Errorf("freePoolMiB = %d, want %d", got, tc.want)
			}
		})
	}

	// The live reading must agree in SHAPE with the capacity reading it
	// mirrors: with every device completely free, free equals pool.
	idle := make([]DeviceMemory, len(discrete))
	for i, device := range discrete {
		device.FreeMiB = device.TotalMiB
		idle[i] = device
	}
	if got, want := freePoolMiB(idle, false, nil), devicePoolMiB(idle, false); got != want {
		t.Errorf("freePoolMiB of an idle machine = %d, devicePoolMiB = %d", got, want)
	}
}
