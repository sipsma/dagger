package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type action struct {
	ID                             int
	Mode, Package, ActionID        string
	Deps                           []int
	TimeReady, TimeStart, TimeDone time.Time
	Cmd                            []string
	CmdReal, CmdUser, CmdSys       int64
}
type kindStats struct {
	Count                                                          int `json:"count"`
	Hits                                                           int `json:"hits"`
	Misses                                                         int `json:"misses"`
	Errors                                                         int `json:"errors"`
	RequestBytes, BodyBytes, ResponseBytes                         int64
	MaterializedBytes, StoreReadBytes, StoreWrittenBytes           int64
	HelperNS, BodyReadNS, QueueNS, DelayNS, StoreNS, MaterializeNS int64
	ResponseWaitNS, ResponseWriteNS                                int64
	HelperP50NS, HelperP95NS, HelperMaxNS                          int64
}
type match struct {
	RequestID int64  `json:"request_id"`
	Command   string `json:"command"`
	ActionID  string `json:"action_id"`
	GraphIDs  []int  `json:"graph_ids,omitempty"`
	Package   string `json:"package,omitempty"`
	Method    string `json:"method"`
}
type visibility struct {
	CacheRequests                 int                       `json:"cache_requests"`
	DirectUnique                  int                       `json:"direct_unique"`
	SubkeyUnique                  int                       `json:"subkey_unique"`
	Ambiguous                     int                       `json:"ambiguous"`
	Unmapped                      int                       `json:"unmapped"`
	DirectFraction                float64                   `json:"direct_fraction"`
	DirectOrKnownSubkeyFraction   float64                   `json:"direct_or_known_subkey_fraction"`
	BuildActions                  int                       `json:"build_actions"`
	BuildActionsWithDirectRequest int                       `json:"build_actions_with_direct_request"`
	ActionsWithCommands           int                       `json:"actions_with_commands"`
	ByKind                        map[string]map[string]int `json:"by_kind"`
}
type analysis struct {
	ByKind     map[string]*kindStats `json:"by_kind"`
	Wire       map[string]int64      `json:"wire_bytes,omitempty"`
	Visibility visibility            `json:"visibility"`
	Notes      []string              `json:"notes"`
}

func analyzeMain(args []string) error {
	f := flag.NewFlagSet("analyze", flag.ContinueOnError)
	dir := f.String("dir", "", "one build's artifact directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	_, err := analyze(*dir)
	return err
}
func readEvents(path string) ([]event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 2<<20)
	for sc.Scan() {
		var ev event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, sc.Err()
}
func graphPrefix(id []byte) string {
	if len(id) != 32 {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(id[:15])
}
func subkey(id []byte, desc string) []byte {
	h := sha256.New()
	h.Write([]byte("subkey:"))
	h.Write(id)
	h.Write([]byte(desc))
	return h.Sum(nil)
}
func analyze(dir string) (*analysis, error) {
	data, err := os.ReadFile(filepath.Join(dir, "graph.json"))
	if err != nil {
		return nil, err
	}
	var acts []action
	if err := json.Unmarshal(data, &acts); err != nil {
		return nil, err
	}
	events, err := readEvents(filepath.Join(dir, "requests.jsonl"))
	if os.IsNotExist(err) {
		events = nil
	} else if err != nil {
		return nil, err
	}
	byPrefix := map[string][]action{}
	for _, a := range acts {
		if a.ActionID != "" {
			byPrefix[a.ActionID] = append(byPrefix[a.ActionID], a)
		}
	}
	// Infer only documented source-known subkeys, never package ownership by
	// timing proximity. Go's action graph has 120-bit prefixes, not full keys.
	type candidate struct {
		a      action
		method string
	}
	known := map[string][]candidate{}
	seenFull := map[string]bool{}
	for _, ev := range events {
		id, err := hex.DecodeString(ev.ActionID)
		if err != nil || len(id) != 32 {
			continue
		}
		as := byPrefix[graphPrefix(id)]
		if len(as) != 1 || seenFull[ev.ActionID] {
			continue
		}
		seenFull[ev.ActionID] = true
		for _, desc := range []string{"stdout", "link-stdout", "srcfiles", "vetout", "_cgo_gotypes.go", "_cgo_import.go", "cgo.cflags", "cgo.ldflags", "cgo.gofiles", "fix.zip"} {
			k := hex.EncodeToString(subkey(id, desc))
			known[k] = append(known[k], candidate{as[0], "known-subkey:" + desc})
		}
	}
	res := &analysis{ByKind: map[string]*kindStats{}, Visibility: visibility{ByKind: map[string]map[string]int{}},
		Notes: []string{
			"Direct matches use the action graph's 120-bit ActionID prefix; they are diagnostic correlations, never cache keys.",
			"Known subkeys reproduce Go 1.26's subkey hash for a limited explicit list; unmatched requests remain unattributed.",
			"Graph TimeStart/TimeDone bracket Go actions including cache I/O, not just compiler work. CmdReal is aggregate command elapsed time, not exact per-command start/end.",
			"Helper durations overlap across requests and nested Go work; their sum is not build wall time.",
			"Protocol headers are counted after receipt; helper time excludes waiting for the next request header, Go-side encoding/hash time and caller-side response handling.",
		}}
	if b, err := os.ReadFile(filepath.Join(dir, "requests.jsonl.wire.json")); err == nil {
		if err := json.Unmarshal(b, &res.Wire); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	times := map[string][]int64{}
	var matches []match
	covered := map[int]bool{}
	for _, ev := range events {
		st := res.ByKind[ev.Command]
		if st == nil {
			st = &kindStats{}
			res.ByKind[ev.Command] = st
		}
		st.Count++
		if ev.Command == "get" {
			if ev.Miss {
				st.Misses++
			} else {
				st.Hits++
			}
		}
		if ev.Error != "" {
			st.Errors++
		}
		st.RequestBytes += ev.RequestBytes
		st.BodyBytes += ev.BodyBytes
		st.ResponseBytes += ev.ResponseBytes
		st.MaterializedBytes += ev.MaterializedBytes
		st.StoreReadBytes += ev.StoreReadBytes
		st.StoreWrittenBytes += ev.StoreWrittenBytes
		st.HelperNS += ev.HelperNS
		st.BodyReadNS += ev.BodyReadNS
		st.QueueNS += ev.QueueNS
		st.DelayNS += ev.DelayNS
		st.StoreNS += ev.StoreNS
		st.MaterializeNS += ev.MaterializeNS
		st.ResponseWaitNS += ev.ResponseWaitNS
		st.ResponseWriteNS += ev.ResponseWriteNS
		times[ev.Command] = append(times[ev.Command], ev.HelperNS)
		if ev.Command != "get" && ev.Command != "put" {
			continue
		}
		res.Visibility.CacheRequests++
		m := match{RequestID: ev.ID, Command: ev.Command, ActionID: ev.ActionID, Method: "unmapped"}
		id, _ := hex.DecodeString(ev.ActionID)
		as := byPrefix[graphPrefix(id)]
		switch {
		case len(as) == 1:
			m.Method = "direct-prefix"
			m.Package = as[0].Package
			m.GraphIDs = []int{as[0].ID}
			covered[as[0].ID] = true
			res.Visibility.DirectUnique++
		case len(as) > 1:
			m.Method = "ambiguous"
			for _, a := range as {
				m.GraphIDs = append(m.GraphIDs, a.ID)
			}
			res.Visibility.Ambiguous++
		case len(known[ev.ActionID]) == 1:
			c := known[ev.ActionID][0]
			m.Method = c.method
			m.Package = c.a.Package
			m.GraphIDs = []int{c.a.ID}
			res.Visibility.SubkeyUnique++
		case len(known[ev.ActionID]) > 1:
			m.Method = "ambiguous"
			res.Visibility.Ambiguous++
		default:
			res.Visibility.Unmapped++
		}
		if res.Visibility.ByKind[ev.Command] == nil {
			res.Visibility.ByKind[ev.Command] = map[string]int{}
		}
		res.Visibility.ByKind[ev.Command][m.Method]++
		matches = append(matches, m)
	}
	for _, a := range acts {
		if a.Mode == "build" && a.Package != "" {
			res.Visibility.BuildActions++
			if covered[a.ID] {
				res.Visibility.BuildActionsWithDirectRequest++
			}
		}
		if len(a.Cmd) > 0 {
			res.Visibility.ActionsWithCommands++
		}
	}
	for k, ts := range times {
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		st := res.ByKind[k]
		st.HelperP50NS = ts[(len(ts)-1)/2]
		st.HelperP95NS = ts[(len(ts)-1)*95/100]
		st.HelperMaxNS = ts[len(ts)-1]
	}
	if n := res.Visibility.CacheRequests; n > 0 {
		res.Visibility.DirectFraction = float64(res.Visibility.DirectUnique) / float64(n)
		res.Visibility.DirectOrKnownSubkeyFraction = float64(res.Visibility.DirectUnique+res.Visibility.SubkeyUnique) / float64(n)
	}
	if err := writeJSON(filepath.Join(dir, "request-packages.json"), matches); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(dir, "package-records.json"), acts); err != nil {
		return nil, err
	}
	return res, writeJSON(filepath.Join(dir, "analysis.json"), res)
}
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}
func checkAnalysis(a *analysis, phase, arm string) error {
	if arm == "plain" {
		return nil
	}
	for k, s := range a.ByKind {
		if s.Errors != 0 {
			return fmt.Errorf("%s %s: %d %s errors", arm, phase, s.Errors, k)
		}
	}
	gets, close := a.ByKind["get"], a.ByKind["close"]
	if gets == nil || gets.Count == 0 || close == nil || close.Count != 1 {
		return fmt.Errorf("missing gets/close in %s %s", arm, phase)
	}
	if a.Wire["stdin_bytes"] == 0 || a.Wire["stdout_bytes"] == 0 {
		return fmt.Errorf("missing raw wire byte accounting")
	}
	if phase == "first" && (a.ByKind["put"] == nil || a.ByKind["put"].BodyBytes == 0) {
		return fmt.Errorf("cold build had no put bodies")
	}
	if phase != "first" && gets.Hits == 0 {
		return fmt.Errorf("warm build had no hits")
	}
	if a.Visibility.DirectUnique == 0 {
		return fmt.Errorf("no action graph correlation; inspect toolchain version")
	}
	return nil
}
