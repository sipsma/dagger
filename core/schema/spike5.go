package schema

import (
	"context"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
)

func installSpike5(srv *dagql.Server) {
	if !core.Spike5Enabled() {
		return
	}
	dagql.Fields[*core.Container]{
		dagql.NodeFunc("withSpike5FileView", func(ctx context.Context, parent dagql.ObjectResult[*core.Container], args struct {
			Files []core.FileID
			Paths []string
		}) (dagql.ObjectResult[*core.Container], error) {
			var zero dagql.ObjectResult[*core.Container]
			files, err := dagql.LoadIDResults(ctx, srv, args.Files)
			if err != nil {
				return zero, err
			}
			ctr, err := core.Spike5FileView(ctx, parent, files, args.Paths)
			if err != nil {
				return zero, err
			}
			return dagql.NewObjectResultForCurrentCall(ctx, srv, ctr)
		}).IsPersistable().Doc("Spike 5 only: a bulk read-only file view backed by ordinary bind mounts."),
	}.Install(srv)
}
