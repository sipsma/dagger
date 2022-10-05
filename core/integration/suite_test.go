package core

import "go.dagger.io/dagger/internal/testutil"

func init() {
	if err := testutil.SetupDaggerd(); err != nil {
		panic(err)
	}
}
