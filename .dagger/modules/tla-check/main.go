// TLA+ model checking for the dagql cache spec (dagql/tla).
//
// Runs every TLC configuration of CacheLifecycle.tla. Green configurations
// are regression checks: any violation fails the check. A configuration may
// name an expected invariant only when it deliberately mutates behavior to
// prove that the check detects the bug, or tracks an accepted model finding.
package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"dagger/tla-check/internal/dagger"
)

const (
	// Pinned TLC release. Same jar and same invocation as documented in
	// dagql/tla/README.md, so local and CI runs are identical.
	tla2toolsURL    = "https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar"
	tla2toolsSHA256 = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88"
	javaBaseImage   = "eclipse-temurin:21-jre"
)

// temporalOutcome is the expected outcome of a configuration that must
// violate a temporal property. TLC does not name the violated property, so
// a liveness mutation can only be gated on the class of failure.
const temporalOutcome = "temporal"

// expectedOutcome maps every configuration to what TLC must report:
// "" means the run must complete with no error found; temporalOutcome means
// a temporal property must be violated; any other value names the one
// invariant or action property that must be violated.
var expectedOutcome = map[string]string{
	"snapshot_import": "",
	"snapshot_export": "",
	// Per-object downloads (fetching chain layers ahead, joining, retrying
	// another import's failed download). Each mutation re-breaks one
	// property; see the config headers.
	"snapshot_download":            "",
	"snapshot_download_droppin":    "JoinedBlobPresent",
	"snapshot_download_leaveearly": "ApplyHasBytes",
	"snapshot_download_ownretry":   "NoOwnRetry",
	"snapshot_download_aheadreuse": "ReuseBeforeBytes",
	// Downloads ahead over a window of later layers (parallel layer
	// downloads), and the mutation that keeps them past a failure.
	"snapshot_download_window":           "",
	"snapshot_download_window_keepahead": "CleanupComplete",
	// green: regression checks over the modeled cache behavior. (The
	// former core configuration is folded into resources: same bounds,
	// every core invariant, and strictly more behavior.)
	"release_prune":         "",
	"liveness":              "",
	"lazy":                  "",
	"lazy_liveness":         "",
	"lazy_stale_cancel":     "",
	"lazy_import":           "",
	"persist":               "",
	"persist_liveness":      "",
	"flush_roundtrip":       "",
	"orphan_edges":          "",
	"release_claim_race":    "",
	"drain_orphan":          "",
	"rollback":              "",
	"rollback_decode":       "",
	"lost_cancel":           "",
	"attach_error":          "",
	"attach_error_adoption": "",
	"attach_error_restart":  "",
	"flush_closure":         "",
	"release_inflight":      "",
	"drain_nested_call":     "",
	"flush_inflight":        "",
	"flush_drained":         "",
	"lazy_release":          "",
	"release_wait":          "",
	// mutation: the last canceling waiter must release a completed fn's leases
	"orphaned_lease": "SharedLeaseReleasedWhenRetired",

	// green: per-part evaluation (stage 2). Attempts are per
	// (result, group); parts map to groups; the metadata-first ordering is
	// enforced before a group's attempt exists; delegation bodies demand
	// dependency parts from inside a running body. See the config headers
	// for the recorded probe and re-break evidence.
	"lazy_parts":          "",
	"lazy_parts_prereq":   "",
	"lazy_parts_liveness": "",
	"lazy_parts_delegate": "",
	"lazy_parts_release":  "",

	// Container completion capture and independent local snapshot opening.
	"container_part_restart":  "",
	"container_sweep_restart": "",
	"container_joint_restore": "",

	// green: reader cancellation inside the persisted-decode singleflight.
	// A joiner that wakes on a departed leader's cancellation retries
	// instead of failing (persistDecodeRetry), and a post-install failure
	// leaves persistLeaseSyncPending set so the next demand retries the
	// lease sync; see the config headers.
	"decode_cancel":          "",
	"decode_cancel_liveness": "",

	// green: session-resource validation (the filter in
	// LookupHit/CanonicalPick/FnComplete, PubIndexFresh/PubAttachAddDep
	// maintenance, BindResource, RequiredExact and ReturnedResourcesBound).
	// resources_restart additionally checks the import-time accounting:
	// the dependency-first required recompute at import and the decode
	// installs leaving the stored set alone; see the config headers.
	"resources":         "",
	"resources_restart": "",
	// green: explicit retention edges on already-published results
	// (AddExplicitDependency) accept requirement-carrying deps; the
	// grown stored set cascades to the parent's ancestors and
	// RequiredExact holds the accounting exact. See the config header.
	"resources_latedep": "",

	// green: requirement growth after the lookup filter. The stored set
	// can grow after a hit was selected (an attached dep while
	// attachment is in flight, or a requirement-carrying retention edge
	// after settling); the serve re-validates by the requirement
	// generation captured at selection and converts a stale hit to a
	// miss. resources_requirement_growth covers the attachment window,
	// resources_latedep_recheck the retention-edge window and
	// resources_latedep_cascade the ancestor cascade, each from an
	// imported starting graph; see the config headers.
	"resources_requirement_growth": "",
	"resources_latedep_recheck":    "",
	"resources_latedep_cascade":    "",

	// green: a session's release can no longer manufacture a failure for
	// a live, innocent caller through the attachment machinery. The
	// publisher's own release still fails the publisher, but its barrier
	// error is classified so parked cross-session readers convert to a
	// miss and execute the call themselves; and attachment targets are
	// always pinned for the session (the claim-at-acquisition invariant,
	// with the claim running before the unlocked refresh), so no other
	// session's release can collect a target out from under its claim.
	// See the config header.
	"attach_release_reader": "",

	// green: one current entry per recipe. Publication adopts the
	// recipe's live entry (D1) when the session covers its requirements,
	// registers beside it unindexed otherwise, and waits for one still
	// attaching; an
	// expired entry is replaced in place when nothing uses it, including
	// no dependent entry, and retired otherwise (D9); a failed
	// replacement drops its retention edge; retirement and the
	// replacement count survive a restart; a live merge targets the
	// recipe's current entry. OneLiveEntryPerCall and the action property
	// NoReplaceUnderUser hold. See the config headers.
	"recipe_adopt":          "",
	"recipe_adopt_filter":   "",
	"recipe_adopt_liveness": "",
	"recipe_expiry":         "",
	"recipe_expiry_deps":    "",
	"recipe_expiry_restart": "",
	"recipe_merge":          "",

	// green: declined persistence (DeclinePersistence). A value its call
	// declined to persist never gains a retention edge: not at
	// publication, not by a later persistable hit, not by a call that
	// adopts it, not by a replacement in place, and not across a restart.
	// The pass configurations are scoped (live, expiry and restart
	// scenarios); each mutation drops one guard, or Flush's saving of the
	// mark, and must break DeclinedNeverPersisted. See the config headers.
	"persist_decline_live":     "",
	"persist_decline_expiry":   "",
	"persist_decline_restart":  "",
	"persist_decline_hitguard": "DeclinedNeverPersisted",
	"persist_decline_adopt":    "DeclinedNeverPersisted",
	"persist_decline_retire":   "DeclinedNeverPersisted",
	"persist_decline_flush":    "DeclinedNeverPersisted",

	// green: call entries (cache_call_record.go). A module function call
	// that returns another call's result is recorded as an entry of its
	// own whose value is the returned entry: lookup serves the returned
	// entry, never the call entry; the call entry owns the returned entry,
	// is merged and restored like any record, and is never decoded. Each
	// mutation drops one rule; each probe must reach its witness. See the
	// config headers.
	"callrec_publish":            "",
	"callrec_expiry":             "",
	"callrec_merge":              "",
	"callrec_restart":            "",
	"callrec_serve":              "CallEntryNeverServed",
	"callrec_nodep":              "CallEntryHoldsReturned",
	"callrec_replace_link":       "CallEntryHoldsReturned",
	"callrec_merge_link":         "CallEntryHoldsReturned",
	"callrec_restore_decode":     "CallEntryNeverDecoded",
	"callrec_owns_once":          "OwnershipExact",
	"callrec_adopt":              "CallEntryNeverServed",
	"probe_callrec_recorded":     "ProbeCallEntryRecorded",
	"probe_callrec_serves":       "ProbeHitWithCallEntry",
	"probe_callrec_merged":       "ProbeMergedCallEntry",
	"probe_callrec_restored":     "ProbeCallEntryRestored",
	"probe_callrec_replaced":     "ProbeCallEntryReplaced",
	"probe_callrec_value_expiry": "ProbeValueExpiryReplace",
}

var clientExpectedOutcome = map[string]string{
	"core":                  "",
	"shared_work":           "",
	"child":                 "",
	"children":              "",
	"grandchild":            "",
	"teardown":              "",
	"blocking_registration": temporalOutcome,
}

type TlaCheck struct {
	// The dagql/tla spec directory.
	Source *dagger.Directory

	// The engine/server/tla spec directory.
	ClientSource *dagger.Directory
}

func New(ws *dagger.Workspace) *TlaCheck {
	return &TlaCheck{
		Source:       ws.Directory("/dagql/tla", dagger.WorkspaceDirectoryOpts{}),
		ClientSource: ws.Directory("/engine/server/tla", dagger.WorkspaceDirectoryOpts{}),
	}
}

// base returns a container with Java, the checksum-verified TLC jar, and
// the spec directory mounted.
func (m *TlaCheck) base(source *dagger.Directory) *dagger.Container {
	jar := dag.HTTP(tla2toolsURL)
	return dag.Container().
		From(javaBaseImage).
		WithFile("/tla2tools.jar", jar).
		WithExec([]string{"sh", "-c",
			fmt.Sprintf("echo '%s  /tla2tools.jar' | sha256sum -c -", tla2toolsSHA256)}).
		WithDirectory("/spec", source).
		WithWorkdir("/spec")
}

// quickConfigs is the curated cheap subset for Quick: every configuration
// that finishes in seconds (roughly 100k distinct states or fewer). It
// catches a spec that stops parsing and registration drift without paying
// for the big state spaces. Keep it in sync when configurations are added
// or their costs change materially.
var quickConfigs = []string{
	"drain_nested_call",
	"drain_orphan",
	"flush_closure",
	"flush_drained",
	"flush_inflight",
	"flush_roundtrip",
	"lazy_liveness",
	"lazy_parts_liveness",
	"lazy_parts_release",
	"lazy_release",
	"lazy_stale_cancel",
	"liveness",
	"lost_cancel",
	"orphan_edges",
	"attach_error",
	"attach_error_restart",
	"release_inflight",
	"release_claim_race",
}

// CacheLifecycle model-checks every configuration of the dagql cache spec
// and verifies each outcome against its expectation.
//
// WARNING: the full run is expensive - well over an hour wall with four
// TLC JVMs, and the largest configurations reach more than 110 million
// distinct states each. Run it sparingly: it is required before pushing changes
// under dagql/tla (it no longer runs in CI), but for iteration prefer
// Quick (seconds), Some (chosen configurations with their expectations
// enforced), or One (a single configuration, raw output, optional probe
// injection).
// +check
func (m *TlaCheck) CacheLifecycle(ctx context.Context) error {
	names := make([]string, 0, len(expectedOutcome))
	for name := range expectedOutcome {
		names = append(names, name)
	}
	return m.runConfigs(ctx, names, defaultTLC)
}

// Quick model-checks only the cheap configurations (quickConfigs), with
// their expectations enforced. It finishes in about a minute and is the
// right default while iterating; it does not replace the full
// CacheLifecycle run before a push.
// +check
func (m *TlaCheck) Quick(ctx context.Context) error {
	return m.runConfigs(ctx, quickConfigs, defaultTLC)
}

// Some model-checks the named configurations (without the CacheLifecycle_
// prefix), with their expectations enforced - the middle ground between
// the full check and One, which enforces nothing.
func (m *TlaCheck) Some(
	ctx context.Context,
	// configuration names without the CacheLifecycle_ prefix, e.g.
	// "resources,resources_latedep"
	configs []string,
	// +optional
	// +default=4
	// how many TLC JVMs run at once
	parallel int,
	// +optional
	// +default="8g"
	// each TLC JVM's maximum heap, as java's -Xmx takes it
	heap string,
	// +optional
	// +default="auto"
	// TLC's worker threads per JVM: a count, or "auto"
	workers string,
	// +optional
	// +default=0
	// stop TLC once its working directory passes this many MB, reported as
	// that configuration's failure, never a pass; 0 is no limit
	diskLimitMb int,
) error {
	if len(configs) == 0 {
		return fmt.Errorf("some: no configurations named")
	}
	opts := tlcOptions{parallel: parallel, heap: heap, workers: workers, diskLimitMB: diskLimitMb}
	if err := opts.validate(); err != nil {
		return fmt.Errorf("some: %w", err)
	}
	var unknown []string
	for _, name := range configs {
		if _, ok := expectedOutcome[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("some: unknown configurations %s (see expectedOutcome in this module)", strings.Join(unknown, ", "))
	}
	return m.runConfigs(ctx, configs, opts)
}

// tlcOptions bound the TLC JVMs a check runs: how many at once, each one's
// heap, its worker threads, and how much disk its working directory may
// take. defaultTLC is what every check ran with before the options existed.
type tlcOptions struct {
	parallel    int
	heap        string
	workers     string
	diskLimitMB int
}

var defaultTLC = tlcOptions{parallel: 4, heap: "8g", workers: "auto"}

var (
	heapPattern    = regexp.MustCompile(`^[0-9]+[kKmMgG]?$`)
	workersPattern = regexp.MustCompile(`^(auto|[0-9]+)$`)
)

// validate refuses options that aren't plain numbers or sizes: they go into
// a shell command line.
func (o tlcOptions) validate() error {
	switch {
	case o.parallel < 1:
		return fmt.Errorf("parallel %d: at least one JVM must run", o.parallel)
	case !heapPattern.MatchString(o.heap):
		return fmt.Errorf("heap %q: want a size such as 6g", o.heap)
	case !workersPattern.MatchString(o.workers):
		return fmt.Errorf("workers %q: want a count or auto", o.workers)
	case o.diskLimitMB < 0:
		return fmt.Errorf("disk limit %d MB: want 0 (none) or more", o.diskLimitMB)
	}
	return nil
}

func (m *TlaCheck) runConfigs(ctx context.Context, names []string, opts tlcOptions) error {
	base := m.base(m.Source)
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)

	var (
		mu       sync.Mutex
		failures []runFailure
		wg       sync.WaitGroup
		// Each configuration is a TLC JVM of several GiB; unbounded fan-out
		// over 30 configurations exhausted a 64 GiB host.
		sem = make(chan struct{}, opts.parallel)
	)
	for _, name := range sorted {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// Snapshot configurations live in SnapshotChain.tla; every other
			// name is a CacheLifecycle configuration (see modelFiles).
			model, cfgPath := modelFiles(name)
			specName := strings.TrimSuffix(model, ".tla")
			configPrefix := specName + "_"
			cfgName := strings.TrimSuffix(strings.TrimPrefix(cfgPath, configPrefix), ".cfg")
			if failure := runOne(ctx, base, specName, configPrefix, cfgName, expectedOutcome[name], opts); failure != nil {
				failure.name = name
				mu.Lock()
				failures = append(failures, *failure)
				mu.Unlock()
			}
		}(name)
	}
	wg.Wait()

	return reportFailures("cache", failures, len(sorted))
}

// ClientLifecycle model-checks client runtime reclamation, typed leases,
// nested-client ownership, authoritative session teardown, and the final
// telemetry barrier.
// +check
func (m *TlaCheck) ClientLifecycle(ctx context.Context) error {
	base := m.base(m.ClientSource)

	var (
		mu       sync.Mutex
		failures []runFailure
		wg       sync.WaitGroup
		// The same JVM fan-out bound as CacheLifecycle.
		sem = make(chan struct{}, 4)
	)
	run := func(group, specName, configPrefix, name, expect string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if failure := runOne(ctx, base, specName, configPrefix, name, expect, defaultTLC); failure != nil {
				failure.name = group + "/" + failure.name
				mu.Lock()
				failures = append(failures, *failure)
				mu.Unlock()
			}
		}()
	}

	clientNames := make([]string, 0, len(clientExpectedOutcome))
	for name := range clientExpectedOutcome {
		clientNames = append(clientNames, name)
	}
	sort.Strings(clientNames)
	for _, name := range clientNames {
		run("lifecycle", "ClientLifecycle", "ClientLifecycle_", name, clientExpectedOutcome[name])
	}

	wg.Wait()

	return reportFailures("client lifecycle", failures, len(clientNames))
}

// One runs a single TLC configuration and returns the raw TLC output,
// using the same pinned jar and invocation as the CacheLifecycle check.
// Unlike the check, it applies no expectation: violations come back in
// the output for the caller to read.
//
// With invariant set, the configuration's INVARIANTS line is replaced by
// that single invariant, the specification is forced to the safety-only
// Spec, and PROPERTY lines are dropped, so one question runs in
// isolation. With define also set, the given TLA+ operator definition is
// appended to the spec first; that runs a scratch probe invariant (for
// example a reachability probe expected to violate) without editing the
// repository.
func (m *TlaCheck) One(
	ctx context.Context,
	// configuration name without the CacheLifecycle_ prefix, e.g. "lazy"
	config string,
	// +optional
	// invariant to check instead of the configuration's INVARIANTS line
	invariant string,
	// +optional
	// TLA+ operator definition to append to the spec, e.g. "ProbeX == ..."
	define string,
) (string, error) {
	if define != "" && invariant == "" {
		return "", fmt.Errorf("define requires invariant: name which invariant to check")
	}
	ctr := m.base(m.Source)

	if define != "" {
		model, _ := modelFiles(config)
		spec, err := m.Source.File(model).Contents(ctx)
		if err != nil {
			return "", fmt.Errorf("read spec: %w", err)
		}
		// The module body ends at the last ==== line; definitions must sit
		// above it.
		term := strings.LastIndex(spec, "\n====")
		if term < 0 {
			return "", fmt.Errorf("spec terminator not found")
		}
		spec = spec[:term] + "\n" + define + "\n" + spec[term:]
		ctr = ctr.WithNewFile("/spec/"+model, spec)
	}

	model, cfgPath := modelFiles(config)
	if invariant != "" {
		cfg, err := m.Source.File(cfgPath).Contents(ctx)
		if err != nil {
			return "", fmt.Errorf("read config %s: %w", cfgPath, err)
		}
		lines := strings.Split(cfg, "\n")
		kept := lines[:0]
		for _, line := range lines {
			switch {
			case strings.HasPrefix(line, "SPECIFICATION"):
				kept = append(kept, "SPECIFICATION Spec")
			case strings.HasPrefix(line, "INVARIANTS"):
				kept = append(kept, "INVARIANTS "+invariant)
			case strings.HasPrefix(line, "PROPERTY"):
				// dropped: a lone invariant is a safety question
			default:
				kept = append(kept, line)
			}
		}
		ctr = ctr.WithNewFile("/spec/"+cfgPath, strings.Join(kept, "\n"))
	}

	// -Xmx8g: the JVM's default heap is a quarter of host memory, so four
	// concurrent configurations could still overcommit a 64 GiB host.
	cmd := fmt.Sprintf(
		"java -Xmx8g -XX:+UseParallelGC -cp /tla2tools.jar tlc2.TLC -workers auto -deadlock -config %s %s 2>&1; true",
		cfgPath, model)
	return ctr.WithExec([]string{"sh", "-c", cmd}).Stdout(ctx)
}

type runFailure struct {
	name    string
	summary string
	detail  string
}

// reportFailures prints the detailed diagnostics and returns a short error
// suitable for propagation through the check API.
func reportFailures(model string, failures []runFailure, total int) error {
	if len(failures) == 0 {
		return nil
	}

	sort.Slice(failures, func(i, j int) bool {
		return failures[i].name < failures[j].name
	})

	fmt.Printf("TLA+ %s model check failures:\n", model)
	for _, failure := range failures {
		fmt.Printf("- %s: %s\n", failure.name, failure.summary)
	}
	for _, failure := range failures {
		if failure.detail == "" {
			continue
		}
		fmt.Printf("\n--- details for %s ---\n%s", failure.name, failure.detail)
		if !strings.HasSuffix(failure.detail, "\n") {
			fmt.Println()
		}
		fmt.Printf("--- end details for %s ---\n\n", failure.name)
	}

	return fmt.Errorf("TLA+ %s model check failed: %d of %d configurations failed", model, len(failures), total)
}

// runOne executes one TLC configuration and returns nil on the expected
// outcome, or detailed diagnostics for reportFailures to print. TLC exits
// nonzero on violations, so the exec swallows the exit code and the output is
// parsed instead.
func runOne(
	ctx context.Context,
	base *dagger.Container,
	specName,
	configPrefix,
	name,
	expect string,
	opts tlcOptions,
) *runFailure {
	out, err := base.WithExec([]string{"sh", "-c", tlcCommand(opts, configPrefix, name, specName)}).Stdout(ctx)
	if err != nil {
		return &runFailure{name: name, summary: "could not run TLC", detail: err.Error()}
	}
	if strings.Contains(out, diskGuardMarker) {
		return &runFailure{
			name:    name,
			summary: fmt.Sprintf("stopped by the disk guard: TLC's working directory passed %d MB, so the configuration is unchecked", opts.diskLimitMB),
			detail:  out,
		}
	}

	clean := strings.Contains(out, "No error has been found")
	violated := ""
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "Error: Invariant "); ok {
			// The invariant name is the first word; the rest is either
			// " is violated." or " is violated by the initial state:".
			violated = strings.Fields(rest)[0]
			break
		}
		if rest, ok := strings.CutPrefix(line, "Error: Action property "); ok {
			// An action property ([][A]_vars) is named like an invariant:
			// " is violated."
			violated = strings.Fields(rest)[0]
			break
		}
		if strings.HasPrefix(line, "Error: Temporal properties were violated") {
			violated = temporalOutcome
			break
		}
	}

	describe := func(outcome string) string {
		if outcome == temporalOutcome {
			return "a temporal property"
		}
		return outcome
	}

	switch {
	case expect == "" && clean:
		return nil
	case expect == "" && violated != "":
		return &runFailure{
			name:    name,
			summary: fmt.Sprintf("expected a clean pass, but %s was violated — a regression in the modeled behavior or the spec", describe(violated)),
			detail:  out,
		}
	case expect != "" && violated == expect:
		return nil
	case expect != "" && clean:
		return &runFailure{
			name:    name,
			summary: fmt.Sprintf("expected %s to be violated, but the run came up clean — the model or config no longer reproduces it", describe(expect)),
			detail:  out,
		}
	case expect != "" && violated != "":
		return &runFailure{
			name:    name,
			summary: fmt.Sprintf("expected %s to be violated, but %s was violated instead — the configuration drifted", describe(expect), describe(violated)),
			detail:  out,
		}
	default:
		return &runFailure{
			name:    name,
			summary: "unrecognized TLC outcome (no clean pass, invariant violation, or temporal violation)",
			detail:  out,
		}
	}
}

// diskGuardMarker is what the guarded command prints when it stops TLC.
const diskGuardMarker = "tla-check disk guard: TLC stopped"

// tlcCommand is the shell command that runs one TLC configuration. The heap
// bound matters: the JVM's default heap is a quarter of host memory, so four
// concurrent configurations could still overcommit a 64 GiB host.
// defaultTLC's -Xmx8g with -workers auto, and no disk limit, is the original
// command line.
//
// With a disk limit, TLC keeps everything it writes in one known directory
// (-metadir): the fingerprint set's spill, the on-disk state queue,
// checkpoints, and the liveness checker's behaviour graph. That directory is
// measured every five seconds; past the limit, TLC's whole process group is
// killed, and the marker follows TLC's output. TLC's output is streamed while
// it runs (tail --pid, which ends once TLC has, after the last line), so a
// run cut short still shows its progress; a tail without --pid falls back to
// printing the output at the end. The output file exists before TLC starts,
// so the tail never opens a missing file. The output is complete after a
// normal exit or a guard kill. An INT or TERM stops TLC's group and drains
// the tail for at most five seconds, so its last lines may be lost. The
// runtime's timeout may SIGKILL the exec instead, which skips this cleanup
// and leaves it to the container's teardown.
func tlcCommand(opts tlcOptions, configPrefix, name, specName string) string {
	if opts.diskLimitMB == 0 {
		return fmt.Sprintf(
			"java -Xmx%s -XX:+UseParallelGC -cp /tla2tools.jar tlc2.TLC -workers %s -deadlock -config %s%s.cfg %s.tla 2>&1; true",
			opts.heap, opts.workers, configPrefix, name, specName)
	}
	return fmt.Sprintf(`meta=/tmp/tlc-meta; mkdir -p $meta
: > /tmp/tlc.out || { echo "tla-check: cannot create /tmp/tlc.out"; exit 1; }
group=; command -v setsid >/dev/null 2>&1 && group=setsid
$group java -Xmx%s -XX:+UseParallelGC -cp /tla2tools.jar tlc2.TLC -workers %s -deadlock -metadir $meta -config %s%s.cfg %s.tla >> /tmp/tlc.out 2>&1 &
pid=$!; tripped=0; tailpid=
stop() { if [ -n "$group" ]; then kill -s KILL -- "-$pid" 2>/dev/null; fi; kill -s KILL "$pid" 2>/dev/null; }
drained=
drain() {
  [ -n "$drained" ] && return; drained=1
  if [ -z "$tailpid" ]; then cat /tmp/tlc.out; return; fi
  i=0; while kill -0 $tailpid 2>/dev/null && [ $i -lt 10 ]; do sleep 0.5; i=$((i+1)); done
  kill $tailpid 2>/dev/null; wait $tailpid 2>/dev/null
}
trap 'stop; drain; exit 143' INT TERM
if tail --help 2>&1 | grep -q -- --pid; then
  tail -n +1 --pid=$pid -f /tmp/tlc.out & tailpid=$!
fi
while kill -0 $pid 2>/dev/null; do
  used=$(du -sk $meta 2>/dev/null | cut -f1)
  if [ "${used:-0}" -gt %d ]; then
    stop; tripped=1; break
  fi
  sleep 5
done
wait $pid 2>/dev/null
drain
if [ $tripped = 1 ]; then echo "%s: its working directory passed %d MB"; fi
true`,
		opts.heap, opts.workers, configPrefix, name, specName,
		opts.diskLimitMB*1024, diskGuardMarker, opts.diskLimitMB)
}

// modelFiles preserves the existing short names and adds the snapshot
// component: snapshot_<name> is SnapshotChain_<name>.cfg.
func modelFiles(name string) (string, string) {
	if rest, ok := strings.CutPrefix(name, "snapshot_"); ok {
		return "SnapshotChain.tla", fmt.Sprintf("SnapshotChain_%s.cfg", rest)
	}
	return "CacheLifecycle.tla", fmt.Sprintf("CacheLifecycle_%s.cfg", name)
}
