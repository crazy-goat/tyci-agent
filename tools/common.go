package tools

var builtInExcludes = []string{
	".git/**",
	"node_modules/**",
	"dist/**",
	"build/**",
	"coverage/**",
	"vendor/**",
	".next/**",
	".cache/**",
	"tmp/**",
	"*.lock",
}

func defaultExcludes(input map[string]any) []string {
	return stringListParam(input, "exclude", builtInExcludes)
}
