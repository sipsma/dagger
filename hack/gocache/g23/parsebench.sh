# Parse time of the Go module's own Dang sources with the engine's vito/dang version, on this host,
# one file at a time (runs in a golang container with git; see run.sh). GOMOD_PIN is the dagger/go
# commit, DANG_VERSION the vito/dang/v2 version in the engine's go.mod.
set -e
git clone -q https://github.com/dagger/go.git /gosrc && git -C /gosrc checkout -q "$GOMOD_PIN"
mkdir -p /pb && cd /pb
go mod init pb >/dev/null 2>&1
go get "github.com/vito/dang/v2@$DANG_VERSION" >/dev/null 2>&1
cat > pb_test.go <<'GO'
package pb

import (
	"os"
	"testing"

	"github.com/vito/dang/v2/pkg/dang"
)

func bench(b *testing.B, path string) {
	src, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := dang.Parse(path, src, dang.GlobalStore("filePath", path)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseGoModule(b *testing.B) { bench(b, "/gosrc/go.dang") }
func BenchmarkParseGomod(b *testing.B)    { bench(b, "/gosrc/gomod/main.dang") }
GO
ls /gosrc/*.dang /gosrc/gomod/*.dang
go test -run '^$' -bench . -benchtime 20x -count 5 .
