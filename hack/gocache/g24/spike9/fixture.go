package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func fixtureMain(args []string) error {
	f := flag.NewFlagSet("fixture", flag.ContinueOnError)
	mode := f.String("mode", "publish", "publish or consume")
	provider := f.String("provider", "", "namespace")
	out := f.String("out", "/out", "reports")
	if err := f.Parse(args); err != nil {
		return err
	}
	store, err := newEngineStore(*provider, "fixture-toolchain/v1")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0755); err != nil {
		return err
	}
	content := bytes.Repeat([]byte("transferable Dagger result\x00\n"), 4096)
	hash := sha256.Sum256(content)
	name := hex.EncodeToString(hash[:])
	fixture := func(op string, ids []string, out any) error {
		var reply struct {
			Value string `json:"_remoteCacheFixture"`
		}
		err := store.query(`query($op:String!,$ids:[ID!]!){_remoteCacheFixture(operation:$op,path:"entries.json",ids:$ids,outputIDs:$ids)}`, map[string]any{"op": op, "ids": ids}, &reply, nil)
		// outputIDs are valid only for export; the other operations use a separate query.
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(reply.Value), out)
	}
	otherFixture := func(op string, out any) error {
		var reply struct {
			Value string `json:"_remoteCacheFixture"`
		}
		path := "entries.json"
		if op == "report" {
			path = ""
		}
		err := store.query(`query($op:String!,$path:String!){_remoteCacheFixture(operation:$op,path:$path)}`, map[string]any{"op": op, "path": path}, &reply, nil)
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(reply.Value), out)
	}
	var before event
	got, err := store.get("entry", &before)
	if err != nil {
		return err
	}
	if got != nil {
		return fmt.Errorf("fixture must start with a missing entry")
	}
	result := map[string]any{"mode": *mode, "before_miss": true, "payload_bytes": len(content), "sha256": name, "real_remote_service": false}
	switch *mode {
	case "publish":
		staging := filepath.Join(*out, "source", name)
		if err := os.MkdirAll(filepath.Dir(staging), 0755); err != nil {
			return err
		}
		if err := os.WriteFile(staging, content, 0644); err != nil {
			return err
		}
		var ids []string
		for _, key := range []string{"entry", "alias"} {
			ev := event{}
			if err := store.put(key, staging, &ev); err != nil {
				return err
			}
			file, err := store.get(key, &ev)
			if err != nil {
				return err
			}
			if file == nil {
				return fmt.Errorf("miss stuck after publication")
			}
			ids = append(ids, file.ID)
		}
		var mapping any
		if err := fixture("export", ids, &mapping); err != nil {
			return err
		}
		result["mapping"] = mapping
		// The copied fixture bundle and chains are the only inputs to the consumer.
		if err := os.RemoveAll(filepath.Dir(staging)); err != nil {
			return err
		}
	case "consume":
		var mapping any
		if err := otherFixture("import", &mapping); err != nil {
			return err
		}
		result["mapping"] = mapping
		for _, key := range []string{"entry", "alias"} {
			ev := event{}
			file, err := store.get(key, &ev)
			if err != nil {
				return err
			}
			if file == nil {
				return fmt.Errorf("transferred key %s missed", key)
			}
			target := filepath.Join(*out, "received-"+key)
			if err := store.export(file.ID, target, &ev); err != nil {
				return err
			}
			data, err := os.ReadFile(target)
			if err != nil {
				return err
			}
			if !bytes.Equal(data, content) {
				return fmt.Errorf("transferred bytes differ")
			}
			if file.Name != name || file.Size != int64(len(content)) {
				return fmt.Errorf("transferred metadata differs")
			}
		}
		var report struct {
			Parts []struct{ Kind string }
			Rows  []json.RawMessage
		}
		if err := otherFixture("report", &report); err != nil {
			return err
		}
		counts := map[string]int{}
		for _, p := range report.Parts {
			counts[p.Kind]++
		}
		if counts["provider-read"] == 0 || counts["installed-chain"] == 0 {
			return fmt.Errorf("fixture did not acquire transferred snapshot: %v", counts)
		}
		result["acquisition_counts"] = counts
		result["consumer_puts"] = 0
	default:
		return fmt.Errorf("unknown fixture mode")
	}
	if err := writeJSON(filepath.Join(*out, "fixture-"+*mode+".json"), result); err != nil {
		return err
	}
	fmt.Printf("PASS fixture %s: miss before publication/import, original keys and bytes checked\n", *mode)
	return nil
}
