package dagger

import "embed"

//go:embed cloak.yaml
//go:embed Dockerfile.daggerd
//go:embed cmd
//go:embed core
//go:embed demos
//go:embed docs
//go:embed embeding.go
//go:embed engine
//go:embed go.mod
//go:embed go.sum
//go:embed internal
//go:embed playground
//go:embed project
//go:embed router
//go:embed sdk
//go:embed secret
//go:embed tracing
var SourceCode embed.FS
