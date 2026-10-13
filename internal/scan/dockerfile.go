package scan

import (
	"os"
	"regexp"
	"strings"
)

// base is the image a Dockerfile's built stage starts from.
type base struct {
	raw string // as written, e.g. python:${PY}-slim
	ref string // with its ARGs filled in
}

var argRef = regexp.MustCompile(`\$\{(\w+)(?::?-([^}]*))?\}|\$(\w+)`)

// baseOf reads which image the stage compose builds starts from: the stage
// named target, else the last one, followed through earlier stages (FROM
// base AS final). args are compose's build args; they win over the
// Dockerfile's ARG defaults. scratch has no base.
func baseOf(path string, args map[string]*string, target string) (base, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return base{}, false
	}
	text := strings.ReplaceAll(string(data), "\\\r\n", " ")
	text = strings.ReplaceAll(text, "\\\n", " ")
	global := map[string]string{} // ARGs before the first FROM: the only ones FROM sees
	stages := map[string]base{}
	var last base
	seenFrom := false
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "ARG":
			if seenFrom {
				continue
			}
			for _, a := range f[1:] {
				k, v, _ := strings.Cut(a, "=")
				global[k] = strings.Trim(v, `"'`)
			}
		case "FROM":
			seenFrom = true
			rest := f[1:]
			for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				continue
			}
			for k, v := range args {
				if v != nil {
					global[k] = *v
				}
			}
			last = base{raw: rest[0], ref: expand(rest[0], global)}
			if b, ok := stages[strings.ToLower(last.ref)]; ok {
				last = b
			}
			if len(rest) >= 3 && strings.EqualFold(rest[1], "AS") {
				name := strings.ToLower(rest[2])
				stages[name] = last
				if target != "" && name == strings.ToLower(target) {
					return found(last)
				}
			}
		}
	}
	if target != "" {
		return base{}, false // the target stage is not there
	}
	return found(last)
}

func found(b base) (base, bool) {
	if b.ref == "" || strings.EqualFold(b.ref, "scratch") || strings.Contains(b.ref, "$") {
		return base{}, false
	}
	return b, true
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
