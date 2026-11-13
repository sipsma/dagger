package remotecache

import (
	"context"
	"testing"

	"github.com/dagger/dagger/dagql"
	"github.com/dagger/dagger/engine/remotecache/protocol"
	"github.com/stretchr/testify/require"
)

// FetchRows asks the service for the entries by their Cloud numbers, with
// decode, and returns its answer's bundle and missing entries.
func TestFetchRows(t *testing.T) {
	s, conn := testSession(t, newFakeAdapter())
	type result struct {
		bundle  dagql.ValueBundle
		missing []uint64
		err     error
	}
	done := make(chan result, 1)
	go func() {
		bundle, missing, err := s.FetchRows(context.Background(), []uint64{7, 9}, true)
		done <- result{bundle, missing, err}
	}()
	env := conn.next(t)
	require.Equal(t, protocol.TypeFetchRows, env.Type)
	require.Equal(t, protocol.FetchRows{Entries: []uint64{7, 9}, Decode: true}, decode[protocol.FetchRows](t, env))
	answer := protocol.FetchedRows{Bundle: dagql.ValueBundle{Version: 9, Roots: []dagql.TransferredRoot{{Ordinal: 1}}}, Missing: []uint64{9}}
	reply, err := protocol.NewEnvelope(900+env.ID, env.ID, protocol.TypeFetchedRows, answer)
	require.NoError(t, err)
	s.dispatch(reply)
	got := within(t, done)
	require.NoError(t, got.err)
	require.Equal(t, answer.Bundle, got.bundle)
	require.Equal(t, []uint64{9}, got.missing)
}

// A fetch whose context ends before the service answers fails with it.
func TestFetchRowsCancelled(t *testing.T) {
	s, conn := testSession(t, newFakeAdapter())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := s.FetchRows(ctx, []uint64{7}, false)
		done <- err
	}()
	require.Equal(t, protocol.TypeFetchRows, conn.next(t).Type)
	cancel()
	require.ErrorIs(t, within(t, done), context.Canceled)
}
