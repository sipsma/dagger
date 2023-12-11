package templates

import (
	"embed"
	"fmt"
	"text/template"

	"github.com/dagger/dagger/cmd/codegen/introspection"
)

//go:embed src
var srcs embed.FS

// New creates a new template with all the template dependencies set up.
func New(schema *introspection.Schema) *template.Template {
	topLevelTemplate := "api"
	templateDeps := []string{
		topLevelTemplate, "header", "objects", "object", "method", "method_solve", "call_args", "method_comment", "types", "args", "default",
	}

	fileNames := make([]string, 0, len(templateDeps))
	for _, tmpl := range templateDeps {
		fileNames = append(fileNames, fmt.Sprintf("src/%s.ts.gtpl", tmpl))
	}

	funcMap := NodejsTemplateFuncs(schema)

	tmpl := template.Must(template.New(topLevelTemplate).Funcs(funcMap).ParseFS(srcs, fileNames...))
	return tmpl
}
