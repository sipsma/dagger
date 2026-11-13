package remotecache

import (
	"context"
	"fmt"
	"time"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
)

// FetchRows asks the service for the Cloud's records of entries the engine
// holds only by identity (dagql.RowFetcher), and waits for the answer, or for
// ctx, which the cache bounds. The answer is merged by the caller.
func (s *session) FetchRows(ctx context.Context, numbers []uint64, decode, frames bool) (dagql.ValueBundle, []uint64, error) {
	id := s.lastID.Add(1)
	sent := time.Now()
	env, err := s.requestAs(ctx, id, protocol.TypeFetchRows, protocol.FetchRows{Entries: numbers, Decode: decode, Frames: frames})
	if err != nil {
		s.cfg.Logger.Warn("remote cache fetch rows failed", "fetch", id, "entries", len(numbers), "decode", decode, "frames", frames, "error", err)
		return dagql.ValueBundle{}, nil, err
	}
	var answer protocol.FetchedRows
	if err := env.Decode(&answer); err != nil {
		return dagql.ValueBundle{}, nil, fmt.Errorf("fetch rows %d: undecodable answer: %w", id, err)
	}
	s.cfg.Logger.Info("remote cache fetch rows",
		"fetch", id,
		"entries", len(numbers),
		"decode", decode,
		"frames", frames,
		"rows", len(answer.Bundle.Values),
		"missing", len(answer.Missing),
		"rtt", time.Since(sent))
	return answer.Bundle, answer.Missing, nil
}
