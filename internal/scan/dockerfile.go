package scan

import (
	"os"
	"regexp"
	"strings"
)

// base is the image a Dockerfile's last stage starts from.
type base struct {
	raw string // as written, e.g. python:${PY}-slim
	ref string // with ARG defaults filled in
}

var argRef = regexp.MustCompile(`\$\{(\w+)(?::?-([^}]*))?\}|\$(\w+)`)

// finalBase reads the FROM of a Dockerfile's last stage, following it
// through earlier stages (FROM base AS final). scratch has none.
func finalBase(path string) (base, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return base{}, false
	}
	text := strings.ReplaceAll(string(data), "\\\r\n", " ")
	text = strings.ReplaceAll(text, "\\\n", " ")
	args := map[string]string{}
	stages := map[string]base{}
	var last base
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "ARG":
			for _, a := range f[1:] {
				k, v, _ := strings.Cut(a, "=")
				if _, set := args[k]; !set || v != "" {
					args[k] = strings.Trim(v, `"'`)
				}
			}
		case "FROM":
			rest := f[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				continue
			}
			last = base{raw: rest[0], ref: expand(rest[0], args)}
			if b, ok := stages[strings.ToLower(last.ref)]; ok { // FROM an earlier stage
				last = b
			}
			if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
				stages[strings.ToLower(rest[2])] = last
			}
		}
	}
	if last.ref == "" || strings.EqualFold(last.ref, "scratch") || strings.Contains(last.ref, "$") {
		return base{}, false
	}
	return last, true
}

func expand(s string, args map[string]string) string {
	return argRef.ReplaceAllStringFunc(s, func(m string) string {
		p := argRef.FindStringSubmatch(m)
		name, def := p[1], p[2]
		if name == "" {
			name = p[3]
		}
		if v := args[name]; v != "" {
			return v
		}
		if def != "" {
			return def
		}
		return m
	})
}
