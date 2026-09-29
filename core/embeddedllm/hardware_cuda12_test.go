package embeddedllm

import (
	"debug/elf"
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// discardLogger returns a logger that swallows the probe's debug output — the
// probe helpers below ProbeHardware assume a non-nil logger (only the public
// entry point normalizes nil), so the direct probe tests provide one.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

// ─────────────────────────────────────────────────────────────────────────────
// Synthetic ELF fixtures

// synthELF builds a minimal 64-bit LSB ELF shared object whose .dynamic table
// carries DT_STRTAB plus — when soname is non-empty — DT_SONAME pointing into
// it. The layout is deliberately hand-rolled: the probe must classify objects
// by their embedded soname, and the fixtures must control that byte-for-byte
// without shipping real (multi-hundred-MB) CUDA libraries into the tree.
// elf.NewFile + DynString read this shape back exactly as they would a real
// library's.
func synthELF(soname string) []byte {
	const (
		ehsize    = 64
		shentsize = 64
		shnum     = 3 // NULL, .dynstr, .dynamic
	)

	dynstr := []byte{0}
	sonameOff := 0
	if soname != "" {
		sonameOff = len(dynstr)
		dynstr = append(dynstr, soname...)
		dynstr = append(dynstr, 0)
	}
	strtabAddr := uint64(ehsize)
	dynamicOff := strtabAddr + uint64(len(dynstr))

	entries := []struct{ tag, val uint64 }{
		{uint64(elf.DT_STRTAB), strtabAddr},
	}
	if soname != "" {
		entries = append([]struct{ tag, val uint64 }{
			{uint64(elf.DT_SONAME), uint64(sonameOff)},
		}, entries...)
	}
	entries = append(entries, struct{ tag, val uint64 }{uint64(elf.DT_NULL), 0})

	dynamicSize := len(entries) * 16
	shstrtab := []byte("\x00.dynstr\x00.dynamic\x00")
	shstrtabOff := dynamicOff + uint64(dynamicSize)
	shoff := shstrtabOff + uint64(len(shstrtab))
	for shoff%8 != 0 {
		shstrtab = append(shstrtab, 0)
		shstrtabOff++
		shoff++
	}

	buf := make([]byte, int(shoff)+shnum*shentsize)
	copy(buf[0:4], "\x7fELF")
	buf[4] = 2 // ELFCLASS64
	buf[5] = 1 // little-endian
	buf[6] = 1 // SYSV
	le := binary.LittleEndian
	le.PutUint16(buf[16:], uint16(elf.ET_DYN))
	le.PutUint16(buf[18:], uint16(elf.EM_X86_64))
	le.PutUint32(buf[20:], 1) // version
	le.PutUint64(buf[40:], shoff)
	le.PutUint32(buf[48:], 0) // flags
	le.PutUint16(buf[52:], ehsize)
	le.PutUint16(buf[54:], 56) // phentsize
	le.PutUint16(buf[56:], 0)  // phnum
	le.PutUint16(buf[58:], shentsize)
	le.PutUint16(buf[60:], shnum)
	le.PutUint16(buf[62:], 0) // shstrndx: section 0, names unused

	section := func(i int, typ uint32, off, size uint64) {
		b := buf[shoff+uint64(i*shentsize):]
		le.PutUint32(b[4:], typ)
		le.PutUint64(b[16:], off)  // sh_addr
		le.PutUint64(b[24:], off)  // sh_offset
		le.PutUint64(b[32:], size) // sh_size
		le.PutUint64(b[48:], 8)    // sh_addralign
	}
	section(1, uint32(elf.SHT_STRTAB), strtabAddr, uint64(len(dynstr)))
	section(2, uint32(elf.SHT_DYNAMIC), dynamicOff, uint64(dynamicSize))
	// .dynamic's sh_link names its string table: section 1.
	le.PutUint32(buf[shoff+2*shentsize+40:], 1)

	copy(buf[strtabAddr:], dynstr)
	for i, e := range entries {
		b := buf[dynamicOff+uint64(i*16):]
		le.PutUint64(b[0:], e.tag)
		le.PutUint64(b[8:], e.val)
	}
	return buf
}

// writeTestELF writes a synthetic library named name (e.g. "libcudart.so.12")
// carrying the given DT_SONAME, returning its path. The FILENAME and the
// SONAME are independent on purpose: that independence is exactly what the
// symlink trap consists of.
func writeTestELF(t *testing.T, dir, name, soname string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, synthELF(soname), 0o644); err != nil {
		t.Fatalf("writing fixture %s: %v", name, err)
	}
	return path
}

// ─────────────────────────────────────────────────────────────────────────────
// Pure halves, table-driven

func TestParseLdconfigEntries(t *testing.T) {
	t.Parallel()

	// The exact shape this machine's cache prints (only .so.13 entries — the
	// mis-packaged state the probe exists to arbitrate).
	const archCache = `libcudart.so.13 (libc6,x86-64) => /opt/cuda/lib64/libcudart.so.13
libcudart.so.13 (libc6,x86-64) => /usr/lib/libcudart.so.13
libcublas.so.13 (libc6,x86-64) => /opt/cuda/lib64/libcublas.so.13
libcublas.so (libc6,x86-64) => /opt/cuda/lib64/libcublas.so
`
	cases := []struct {
		name string
		out  string
		want map[string][]string
	}{
		{
			name: "real two-source cache",
			out:  archCache,
			want: map[string][]string{
				"libcudart.so.13": {"/opt/cuda/lib64/libcudart.so.13", "/usr/lib/libcudart.so.13"},
				"libcublas.so.13": {"/opt/cuda/lib64/libcublas.so.13"},
				"libcublas.so":    {"/opt/cuda/lib64/libcublas.so"},
			},
		},
		{
			name: "same soname twice keeps first-seen order",
			out:  "libfoo.so.1 (libc6,x86-64) => /b/libfoo.so.1\nlibfoo.so.1 (libc6,x86-64) => /a/libfoo.so.1\n",
			want: map[string][]string{"libfoo.so.1": {"/b/libfoo.so.1", "/a/libfoo.so.1"}},
		},
		{
			name: "prose and malformed lines are skipped",
			out:  "128 caches found.\ngarbage line without arrow\n\tindented junk => /tmp/x\n",
			want: map[string][]string{},
		},
		{
			name: "arrow with empty path is skipped",
			out:  "libfoo.so.1 (libc6) => \n",
			want: map[string][]string{},
		},
		{
			name: "crlf endings",
			out:  "libfoo.so.1 (libc6,x86-64) => /usr/lib/libfoo.so.1\r\n",
			want: map[string][]string{"libfoo.so.1": {"/usr/lib/libfoo.so.1\r"}},
		},
		{name: "empty output", out: "", want: map[string][]string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := parseLdconfigEntries(tc.out)
			if len(got) != len(tc.want) {
				t.Fatalf("parseLdconfigEntries returned %d sonames (%v), want %d",
					len(got), got, len(tc.want))
			}
			for soname, wantPaths := range tc.want {
				gotPaths, ok := got[soname]
				if !ok {
					t.Errorf("soname %q missing from result %v", soname, got)
					continue
				}
				if len(gotPaths) != len(wantPaths) {
					t.Errorf("soname %q paths = %v, want %v", soname, gotPaths, wantPaths)
					continue
				}
				for i := range wantPaths {
					if gotPaths[i] != wantPaths[i] {
						t.Errorf("soname %q path[%d] = %q, want %q", soname, i, gotPaths[i], wantPaths[i])
					}
				}
			}
		})
	}
}

func TestCUDA12UserlandCandidates(t *testing.T) {
	t.Parallel()

	// A fake glob resolver: fixed answers per pattern, optionally an error —
	// standing in for filepath.Glob so the merge is hermetic.
	fakeGlob := func(matches map[string][]string, failPattern string) func(string) ([]string, error) {
		return func(pattern string) ([]string, error) {
			if pattern == failPattern {
				return nil, os.ErrNotExist
			}
			return matches[pattern], nil
		}
	}
	const cudartGlob = "/usr/lib/libcudart.so.12*"
	const cublasGlob = "/usr/lib/libcublas.so.12*"

	cases := []struct {
		name    string
		lib     string
		ldcache map[string][]string
		globFn  func(string) ([]string, error)
		want    []string
	}{
		{
			name: "cache hit only",
			lib:  "libcudart.so.12",
			ldcache: map[string][]string{
				"libcudart.so.12": {"/cache/libcudart.so.12"},
			},
			globFn: fakeGlob(map[string][]string{}, ""),
			want:   []string{"/cache/libcudart.so.12"},
		},
		{
			name:    "glob hit when the cache under-reports",
			lib:     "libcudart.so.12",
			ldcache: map[string][]string{},
			globFn: fakeGlob(map[string][]string{
				cudartGlob: {"/usr/lib/libcudart.so.12"},
			}, ""),
			want: []string{"/usr/lib/libcudart.so.12"},
		},
		{
			name: "both sources merge with dedup, cache first",
			lib:  "libcudart.so.12",
			ldcache: map[string][]string{
				"libcudart.so.12": {"/shared/libcudart.so.12", "/cache-only/libcudart.so.12"},
			},
			globFn: fakeGlob(map[string][]string{
				cudartGlob: {"/shared/libcudart.so.12", "/glob-only/libcudart.so.12"},
			}, ""),
			want: []string{"/shared/libcudart.so.12", "/cache-only/libcudart.so.12", "/glob-only/libcudart.so.12"},
		},
		{
			name:    "a failing glob does not lose the other sources",
			lib:     "libcublas.so.12",
			ldcache: map[string][]string{},
			globFn: fakeGlob(map[string][]string{
				cublasGlob: {"/usr/lib/libcublas.so.12"},
			}, "/usr/lib/x86_64-linux-gnu/libcublas.so.12*"),
			want: []string{"/usr/lib/libcublas.so.12"},
		},
		{
			name:    "nothing found anywhere",
			lib:     "libcudart.so.12",
			ldcache: map[string][]string{},
			globFn:  fakeGlob(map[string][]string{}, ""),
			want:    nil,
		},
		{
			name: "an unrelated cache entry is not consulted",
			lib:  "libcudart.so.12",
			ldcache: map[string][]string{
				"libcudart.so.13": {"/opt/cuda/lib64/libcudart.so.13"},
			},
			globFn: fakeGlob(map[string][]string{}, ""),
			want:   nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := cuda12UserlandCandidates(tc.lib, tc.ldcache, tc.globFn)
			if len(got) != len(tc.want) {
				t.Fatalf("candidates = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("candidate[%d] = %q, want %q (all: %v)", i, got[i], tc.want[i], got)
				}
			}
		})
	}
}

func TestInspectCUDA12Lib(t *testing.T) {
	t.Parallel()

	t.Run("fixture matrix", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		// Genuine: a real .12 ELF under its own name.
		genuine := writeTestELF(t, dir, "libcudart.so.12", "libcudart.so.12")
		// The trap: a ".so.12"-named symlink whose target is a 13 ELF.
		thirteen := writeTestELF(t, dir, "libcudart.so.13", "libcudart.so.13")
		trap := filepath.Join(dir, "trap-libcudart.so.12")
		if err := os.Symlink(thirteen, trap); err != nil {
			t.Fatalf("creating the trap symlink: %v", err)
		}
		// A stripped object: parsable ELF, no DT_SONAME at all.
		stripped := writeTestELF(t, dir, "stripped-libcudart.so.12", "")
		// Garbage: not an ELF.
		garbage := filepath.Join(dir, "garbage-libcudart.so.12")
		if err := os.WriteFile(garbage, []byte("definitely not an ELF"), 0o644); err != nil {
			t.Fatalf("writing garbage fixture: %v", err)
		}
		// Genuine sibling next to a mismatched one.
		bothDir := t.TempDir()
		writeTestELF(t, bothDir, "libcudart.so.13", "libcudart.so.13")
		if err := os.Symlink(filepath.Join(bothDir, "libcudart.so.13"),
			filepath.Join(bothDir, "libcudart.so.12")); err != nil {
			t.Fatalf("creating the both-dir trap symlink: %v", err)
		}
		both := writeTestELF(t, bothDir, "second-libcudart.so.12", "libcudart.so.12")

		cases := []struct {
			name       string
			candidates []string
			want       cuda12LibVerdict
		}{
			{name: "nothing discovered", candidates: nil, want: cuda12LibMissing},
			{name: "genuine .12 elf", candidates: []string{genuine}, want: cuda12LibGenuine},
			{
				name:       "so.12 symlink to a so.13 elf",
				candidates: []string{trap},
				want:       cuda12LibMismatch,
			},
			{name: "not an elf", candidates: []string{garbage}, want: cuda12LibIndeterminate},
			{name: "elf without dt_soname", candidates: []string{stripped}, want: cuda12LibIndeterminate},
			{name: "absent path", candidates: []string{filepath.Join(dir, "nope.so.12")}, want: cuda12LibIndeterminate},
			{
				// One genuine object settles the library no matter what its
				// mismatched siblings look like.
				name:       "genuine wins over mismatched sibling",
				candidates: []string{trap, genuine},
				want:       cuda12LibGenuine,
			},
			{
				name:       "genuine found after the trap in discovery order",
				candidates: []string{filepath.Join(bothDir, "libcudart.so.12"), both},
				want:       cuda12LibGenuine,
			},
			{
				name:       "mismatch wins over indeterminate sibling",
				candidates: []string{garbage, trap},
				want:       cuda12LibMismatch,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				if got := inspectCUDA12Lib("libcudart.so.12", tc.candidates); got != tc.want {
					t.Errorf("inspectCUDA12Lib = %v, want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("cublas symmetry", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		genuine := writeTestELF(t, dir, "libcublas.so.12", "libcublas.so.12")
		if got := inspectCUDA12Lib("libcublas.so.12", []string{genuine}); got != cuda12LibGenuine {
			t.Errorf("libcublas.so.12 genuine inspection = %v, want genuine", got)
		}
		// The same ELF under the OTHER library's wanted name is a mismatch:
		// sonames are exact, not prefix-matched.
		if got := inspectCUDA12Lib("libcublas.so.12", []string{
			writeTestELF(t, dir, "cublas-cudart.so.12", "libcudart.so.12"),
		}); got != cuda12LibMismatch {
			t.Errorf("cross-library soname inspection = %v, want mismatch", got)
		}
	})
}

func TestClassifyCUDA12Userland(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		cudart       cuda12LibVerdict
		cublas       cuda12LibVerdict
		haveLdconfig bool
		want         CUDA12Userland
	}{
		{
			name: "both genuine", cudart: cuda12LibGenuine, cublas: cuda12LibGenuine,
			haveLdconfig: true, want: CUDA12Present,
		},
		{
			// present does not lean on the cache: the fallback globs are
			// checked on every run.
			name: "both genuine without ldconfig", cudart: cuda12LibGenuine, cublas: cuda12LibGenuine,
			haveLdconfig: false, want: CUDA12Present,
		},
		{
			// The trap machine: both ".so.12" names resolve to 13-series ELFs.
			name: "both mismatched", cudart: cuda12LibMismatch, cublas: cuda12LibMismatch,
			haveLdconfig: true, want: CUDA12Absent,
		},
		{
			name: "cudart genuine, cublas mismatched", cudart: cuda12LibGenuine, cublas: cuda12LibMismatch,
			haveLdconfig: true, want: CUDA12Absent,
		},
		{
			name: "cudart mismatched, cublas genuine", cudart: cuda12LibMismatch, cublas: cuda12LibGenuine,
			haveLdconfig: true, want: CUDA12Absent,
		},
		{
			name: "clean machine, cache answered", cudart: cuda12LibMissing, cublas: cuda12LibMissing,
			haveLdconfig: true, want: CUDA12Absent,
		},
		{
			// Without ldconfig a bare "nothing found" is NOT a definitive
			// absent: discovery itself was incomplete.
			name: "clean machine, cache unreadable", cudart: cuda12LibMissing, cublas: cuda12LibMissing,
			haveLdconfig: false, want: CUDA12Unknown,
		},
		{
			name: "one library uninspectable", cudart: cuda12LibIndeterminate, cublas: cuda12LibGenuine,
			haveLdconfig: true, want: CUDA12Unknown,
		},
		{
			name: "other library uninspectable", cudart: cuda12LibGenuine, cublas: cuda12LibIndeterminate,
			haveLdconfig: true, want: CUDA12Unknown,
		},
		{
			// Indeterminate beats mismatch: unknown outranks absent.
			name: "indeterminate and mismatched", cudart: cuda12LibIndeterminate, cublas: cuda12LibMismatch,
			haveLdconfig: true, want: CUDA12Unknown,
		},
		{
			// A genuine runtime still needs its sibling: one genuine library
			// and one provably missing make a CUDA 12 build unrunnable.
			name: "genuine and missing with a complete scan", cudart: cuda12LibGenuine, cublas: cuda12LibMissing,
			haveLdconfig: true, want: CUDA12Absent,
		},
		{
			name: "genuine and missing with an incomplete scan", cudart: cuda12LibGenuine, cublas: cuda12LibMissing,
			haveLdconfig: false, want: CUDA12Unknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classifyCUDA12Userland(tc.cudart, tc.cublas, tc.haveLdconfig)
			if got != tc.want {
				t.Errorf("classifyCUDA12Userland(%v, %v, %v) = %q, want %q",
					tc.cudart, tc.cublas, tc.haveLdconfig, got, tc.want)
			}
		})
	}
}

// TestCuda12LibVerdictString keeps the probe's log vocabulary stable — the
// support bundles are read by humans.
func TestCuda12LibVerdictString(t *testing.T) {
	t.Parallel()

	cases := map[cuda12LibVerdict]string{
		cuda12LibGenuine:       "genuine",
		cuda12LibMismatch:      "mismatch",
		cuda12LibMissing:       "missing",
		cuda12LibIndeterminate: "indeterminate",
	}
	for v, want := range cases {
		if got := v.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", v, got, want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// I/O halves against real fixtures

func TestELFSONAME(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	genuine := writeTestELF(t, dir, "libgenuine.so.12", "libgenuine.so.12")
	stripped := writeTestELF(t, dir, "libstripped.so.12", "")
	garbage := filepath.Join(dir, "libgarbage.so.12")
	if err := os.WriteFile(garbage, []byte{0x7f, 'E', 'L', 'F', 2}, 0o644); err != nil {
		t.Fatalf("writing truncated fixture: %v", err)
	}
	textfile := filepath.Join(dir, "libtext.so.12")
	if err := os.WriteFile(textfile, []byte("plain text, not an ELF"), 0o644); err != nil {
		t.Fatalf("writing text fixture: %v", err)
	}

	cases := []struct {
		name     string
		path     string
		wantOK   bool
		wantName string
	}{
		{name: "genuine soname", path: genuine, wantOK: true, wantName: "libgenuine.so.12"},
		{name: "elf without dt_soname", path: stripped, wantOK: false},
		{name: "truncated elf", path: garbage, wantOK: false},
		{name: "not an elf", path: textfile, wantOK: false},
		{name: "missing file", path: filepath.Join(dir, "absent.so.12"), wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := elfSONAME(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("elfSONAME(%q) ok = %v (got %q), want %v", tc.path, ok, got, tc.wantOK)
			}
			if tc.wantOK && got != tc.wantName {
				t.Errorf("elfSONAME(%q) = %q, want %q", tc.path, got, tc.wantName)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The probe itself

// TestProbeCUDA12UserlandOnThisMachine pins the verdict on the machine this
// probe was written for: every ".so.12" name here is a symlink onto a
// 13-series ELF, and the ldconfig cache carries only ".so.13" entries — so a
// naive name-based probe would report present while a CUDA 12.x build would
// fail to load. The DT_SONAME check must cut through the trap and answer
// absent. (A clean Linux CI runner answers absent too: no candidates at all,
// with the cache having answered.) darwin/arm64 and windows/amd64 CI skip —
// there the probe deliberately reports unknown without looking.
func TestProbeCUDA12UserlandOnThisMachine(t *testing.T) {
	if !cuda12UserlandProbeSupported {
		t.Skipf("CUDA 12 userland probing is a linux/amd64 verdict; on %s/%s it reports unknown", runtime.GOOS, runtime.GOARCH)
	}
	t.Parallel()

	verdict := probeCUDA12Userland(t.Context(), discardLogger())
	if verdict != CUDA12Absent {
		t.Fatalf("probeCUDA12Userland = %q, want %q: the .so.12 symlinks onto .so.13 ELFs must not read as present",
			verdict, CUDA12Absent)
	}
}

// TestResolveProfileOnThisMachine is the end-to-end harness for the machine
// this work was written on: it runs the LIVE probe, builds the install path's
// exact MachineProfile from it (the plan() shape — Platform, Backend, RAMGiB,
// CUDA12Userland) and resolves it. The pinned outcome: a linux/amd64 box with
// a 13-series driver (cuda-13.3 tag) and NO usable CUDA 12.x userland keeps
// the probed cuda-13.3 build, and the #222 guard is recorded UNAPPLIED with
// the absent-verdict guidance — resolution and guard record agree, and the
// runtime archive is the pinned linux cuda-13.3 one. The earlier probes in
// this file pin the halves; this test pins the pair the installer acts on.
func TestResolveProfileOnThisMachine(t *testing.T) {
	if !cuda12UserlandProbeSupported {
		t.Skipf("the machine profile harness pins a linux/amd64 verdict; on %s/%s the userland probe reports unknown", runtime.GOOS, runtime.GOARCH)
	}
	t.Parallel()

	hw, err := ProbeHardware(t.Context(), discardLogger())
	if err != nil {
		t.Fatalf("ProbeHardware error = %v", err)
	}

	// The machine pins: this is the CUDA-13.4-driver, 31-GiB host with only
	// 13-series CUDA userland that motivated the userland-aware guard. A drift
	// in any of these is a changed machine, not a pass.
	if hw.Platform != PlatformLinuxAMD64 {
		t.Skipf("machine is %q, not the pinned %q platform", hw.Platform, PlatformLinuxAMD64)
	}
	if hw.Backend != BackendCUDA133 {
		t.Fatalf("probed backend = %q, want %q (a driver below 13.3 or a changed accelerator invalidates this pin)", hw.Backend, BackendCUDA133)
	}
	if hw.CUDA12Userland != CUDA12Absent {
		t.Fatalf("CUDA12Userland = %q, want %q: without the absent verdict the #222 substitution is expected to fire and this pin does not apply", hw.CUDA12Userland, CUDA12Absent)
	}
	if hw.RAMGiB < 31 || hw.RAMGiB >= 32 {
		t.Fatalf("RAMGiB = %.2f, want the pinned 31-GiB host (floored)", hw.RAMGiB)
	}

	// The profile install.plan() builds, verbatim.
	res, err := ResolveProfile(MachineProfile{
		Platform:       hw.Platform,
		Backend:        hw.Backend,
		RAMGiB:         hw.RAMGiB,
		CUDA12Userland: hw.CUDA12Userland,
	})
	if err != nil {
		t.Fatalf("ResolveProfile error = %v, want a viable resolution", err)
	}

	if res.Backend != BackendCUDA133 {
		t.Errorf("Resolution.Backend = %q, want the probed %q (no CUDA 12 userland to substitute onto)",
			res.Backend, BackendCUDA133)
	}

	decision := findGuard(t, res.Guards, GuardCUDA133Crash)
	if decision.Applied {
		t.Error("the #222 substitution fired without a CUDA 12 userland; it must stay unapplied")
	}
	if decision.Backend != BackendCUDA128 {
		t.Errorf("#222 target = %q, want the documented %q fallback it declined", decision.Backend, BackendCUDA128)
	}
	if decision.Reason != GuardReasonCrashOnLoad || decision.Severity != GuardSeverityCritical {
		t.Errorf("#222 reason/severity = %q/%q, want %q/%q",
			decision.Reason, decision.Severity, GuardReasonCrashOnLoad, GuardSeverityCritical)
	}
	if !strings.Contains(decision.Guidance, "no usable CUDA 12.x") {
		t.Errorf("#222 guidance does not carry the absent-verdict reason: %q", decision.Guidance)
	}

	assertRuntimeArchive(t, res, "llama-"+RuntimeTag+"-bin-linux-cuda-13.3-x64.tar.gz")
}

// TestProbeHardwareCarriesCUDA12Userland checks the probe is wired into the
// Hardware result and that the tri-state survives the full ProbeHardware path
// on this machine (the same verdict the standalone probe just pinned).
func TestProbeHardwareCarriesCUDA12Userland(t *testing.T) {
	if !cuda12UserlandProbeSupported {
		t.Skipf("CUDA 12 userland probing is a linux/amd64 verdict; on %s/%s it reports unknown", runtime.GOOS, runtime.GOARCH)
	}
	t.Parallel()

	hw, err := ProbeHardware(t.Context(), nil)
	if err != nil {
		t.Fatalf("ProbeHardware error = %v", err)
	}
	if hw.CUDA12Userland != CUDA12Present && hw.CUDA12Userland != CUDA12Absent && hw.CUDA12Userland != CUDA12Unknown {
		t.Fatalf("CUDA12Userland = %q, want one of the three tri-state values", hw.CUDA12Userland)
	}
	if hw.CUDA12Userland != CUDA12Absent {
		t.Errorf("CUDA12Userland = %q, want %q on this machine", hw.CUDA12Userland, CUDA12Absent)
	}
}
