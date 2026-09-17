package dagql

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

type countingTestProvider map[digest.Digest][]byte

type countingTestReaderAt struct{ *bytes.Reader }

func (countingTestReaderAt) Close() error { return nil }

func (p countingTestProvider) Info(context.Context, digest.Digest) (content.Info, error) {
	return content.Info{}, nil
}

func (p countingTestProvider) ReaderAt(_ context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	data, ok := p[desc.Digest]
	if !ok {
		return nil, errors.New("no such blob")
	}
	return countingTestReaderAt{bytes.NewReader(data)}, nil
}

// The download log line reports one open per layer fetched and the bytes
// read through the provider.
func TestCountingContentProvider(t *testing.T) {
	t.Parallel()
	first, second := []byte("first layer bytes"), []byte("second")
	inner := countingTestProvider{digest.FromBytes(first): first, digest.FromBytes(second): second}
	counted := &countingContentProvider{InfoReaderProvider: inner}
	for _, data := range [][]byte{first, second} {
		reader, err := counted.ReaderAt(t.Context(), ocispecs.Descriptor{Digest: digest.FromBytes(data), Size: int64(len(data))})
		require.NoError(t, err)
		buf := make([]byte, len(data))
		n, err := reader.ReadAt(buf, 0)
		require.NoError(t, err)
		require.Equal(t, len(data), n)
		// A second read of the same range counts again: bytes are what the
		// provider served, not the layer's size.
		n, err = reader.ReadAt(buf[:3], 0)
		require.NoError(t, err)
		require.Equal(t, 3, n)
		require.NoError(t, reader.Close())
	}
	_, err := counted.ReaderAt(t.Context(), ocispecs.Descriptor{Digest: digest.FromString("absent")})
	require.Error(t, err)
	require.Equal(t, int64(2), counted.opened.Load(), "a failed open is not counted")
	require.Equal(t, int64(len(first)+len(second)+6), counted.bytes.Load())
	require.Equal(t, `{"part":"snapshot"}`, partAddressString(PersistedPartAddress{Part: "snapshot"}))
}
