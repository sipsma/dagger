package schema

import (
	"context"
	"os"

	"github.com/dagger/dagger/core"
	"github.com/dagger/dagger/dagql"
)

func installSpike9(srv *dagql.Server) {
	if os.Getenv("_DAGGER_SPIKE9") != "1" {
		return
	}
	if query, ok := srv.ObjectType("Query"); ok {
		if _, installed := query.FieldSpec("_spike9Get", srv.View); installed {
			return
		}
	}
	dagql.Fields[*core.Query]{
		dagql.Func("_spike9Get", func(ctx context.Context, _ *core.Query, args core.Spike9Key) (dagql.Nullable[dagql.ObjectResult[*core.File]], error) {
			file, ok, err := core.Spike9Get(ctx, args)
			if err != nil || !ok {
				return dagql.Null[dagql.ObjectResult[*core.File]](), err
			}
			return dagql.NonNull(file), nil
		}).DoNotCache("A miss must observe a later publication").View(AllVersion),
		dagql.Func("_spike9Put", func(ctx context.Context, _ *core.Query, args struct {
			Provider, Toolchain, Key string
			File                     dagql.ID[*core.File]
		}) (dagql.ObjectResult[*core.File], error) {
			file, err := args.File.Load(ctx, srv)
			if err != nil {
				return file, err
			}
			return core.Spike9Put(ctx, core.Spike9Key{Provider: args.Provider, Toolchain: args.Toolchain, Key: args.Key}, file)
		}).DoNotCache("Publication checks the immutable tool-key binding").View(AllVersion),
	}.Install(srv)
}
