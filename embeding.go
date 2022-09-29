package dagger

import "embed"

//go:embed Dockerfile.daggerd
//go:embed LICENSE
//go:embed NOTICE
//go:embed README.md
//go:embed cloak.yaml
//go:embed cmd
//go:embed core
//go:embed demos
//go:embed docs
//go:embed embeding.go
//go:embed engine
//go:embed examples
//go:embed go.mod
//go:embed go.sum
//go:embed internal
//go:embed package.json
//go:embed playground
//go:embed project
//go:embed router
//go:embed sdk
//go:embed secret
//go:embed tracing
//go:embed tsconfig.json
//go:embed website
//go:embed yarn.lock
var SourceCode embed.FS
