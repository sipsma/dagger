package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type engineStore struct {
	provider, toolchain, endpoint, token string
	client                               *http.Client
}
type engineEntry struct {
	ID, Name string
	Size     int64
}

func newEngineStore(provider, toolchain string) (*engineStore, error) {
	port, token := os.Getenv("DAGGER_SESSION_PORT"), os.Getenv("DAGGER_SESSION_TOKEN")
	if port == "" || token == "" || provider == "" || toolchain == "" {
		return nil, fmt.Errorf("Dagger session, provider and toolchain required")
	}
	return &engineStore{provider: provider, toolchain: toolchain, endpoint: "http://127.0.0.1:" + port + "/query", token: token, client: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128}}}, nil
}
func (s *engineStore) query(q string, vars map[string]any, out any, ev *event) error {
	body, err := json.Marshal(map[string]any{"query": q, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(s.token, "")
	res, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if ev != nil {
		ev.EngineRequests++
		ev.EngineRequestBytes += int64(len(body))
		ev.EngineResponseBytes += int64(len(data))
	}
	var reply struct {
		Data   json.RawMessage
		Errors []struct{ Message string }
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return err
	}
	if res.StatusCode != 200 || len(reply.Errors) != 0 {
		return fmt.Errorf("engine status %d: %s", res.StatusCode, data)
	}
	return json.Unmarshal(reply.Data, out)
}
func (s *engineStore) variables(key string) map[string]any {
	return map[string]any{"p": s.provider, "t": s.toolchain, "k": key}
}
func (s *engineStore) get(key string, ev *event) (*engineEntry, error) {
	var out struct {
		File *engineEntry `json:"_spike9Get"`
	}
	err := s.query(`query($p:String!,$t:String!,$k:String!){_spike9Get(provider:$p,toolchain:$t,key:$k){id name size}}`, s.variables(key), &out, ev)
	return out.File, err
}
func (s *engineStore) put(key, path string, ev *event) error {
	before := time.Now()
	var input struct {
		Host struct{ File struct{ ID string } }
	}
	err := s.query(`query($path:String!){host{file(path:$path){id}}}`, map[string]any{"path": path}, &input, ev)
	ev.ImportNS = time.Since(before).Nanoseconds()
	if err != nil {
		return err
	}
	// The id query is lazy; actual filesync import/evaluation is charged to put.
	vars := s.variables(key)
	vars["f"] = input.Host.File.ID
	var out struct {
		File engineEntry `json:"_spike9Put"`
	}
	before = time.Now()
	err = s.query(`query($p:String!,$t:String!,$k:String!,$f:ID!){_spike9Put(provider:$p,toolchain:$t,key:$k,file:$f){id name size}}`, vars, &out, ev)
	ev.PutNS = time.Since(before).Nanoseconds()
	return err
}
func (s *engineStore) export(id, path string, ev *event) error {
	var out any
	return s.query(`query($id:ID!,$path:String!){node(id:$id){... on File{export(path:$path)}}}`, map[string]any{"id": id, "path": path}, &out, ev)
}
