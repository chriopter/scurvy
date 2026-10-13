// Package docker asks the docker CLI what runs here: the containers, the
// images behind them, and the compose projects that started them.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chriopter/scurvy/internal/registry"
)

// Compose labels docker compose puts on every container it starts.
const (
	LabelProject    = "com.docker.compose.project"
	LabelService    = "com.docker.compose.service"
	LabelWorkingDir = "com.docker.compose.project.working_dir"
	LabelConfig     = "com.docker.compose.project.config_files"
	LabelEnvFiles   = "com.docker.compose.project.environment_file"
	LabelOneOff     = "com.docker.compose.oneoff"
)

// Container is a running container.
type Container struct {
	ID      string
	Name    string
	Image   string // the image as configured, e.g. ghcr.io/acme/web:latest
	ImageID string // the image it actually runs
	Labels  map[string]string
}

// Project and Service of the compose project that started it; empty for
// containers started otherwise (docker run, other tools).
func (c Container) Project() string { return c.Labels[LabelProject] }
func (c Container) Service() string { return c.Labels[LabelService] }

// OneOff reports a container of `docker compose run`: it carries its
// service's labels, but `up` would not make it again.
func (c Container) OneOff() bool { return strings.EqualFold(c.Labels[LabelOneOff], "true") }

// Image is a local image.
type Image struct {
	ID          string
	Created     time.Time
	RepoDigests []string // repo@sha256:… for every registry it came from
}

// HasDigest reports whether the image is the one a registry calls digest.
func (i Image) HasDigest(digest string) bool {
	for _, d := range i.RepoDigests {
		if strings.HasSuffix(d, "@"+digest) {
			return true
		}
	}
	return false
}

func run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return out, fmt.Errorf("docker %s: %s", args[0], firstLine(msg))
	}
	return out, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	return s
}

// Running lists the running containers.
func Running(ctx context.Context) ([]Container, error) {
	out, err := run(ctx, "", "ps", "-q", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	out, err = run(ctx, "", append([]string{"inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID     string `json:"Id"`
		Name   string
		Image  string
		Config struct {
			Image  string
			Labels map[string]string
		}
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	cs := make([]Container, 0, len(raw))
	for _, r := range raw {
		cs = append(cs, Container{
			ID:      r.ID,
			Name:    strings.TrimPrefix(r.Name, "/"),
			Image:   r.Config.Image,
			ImageID: r.Image,
			Labels:  r.Config.Labels,
		})
	}
	return cs, nil
}

// Images looks up local images and remembers them for one scan.
type Images struct {
	mu   sync.Mutex
	seen map[string]*Image
}

func NewImages() *Images { return &Images{seen: map[string]*Image{}} }

// Get inspects an image by ID or reference; nil when there is none.
func (im *Images) Get(ctx context.Context, ref string) *Image {
	im.mu.Lock()
	img, ok := im.seen[ref]
	im.mu.Unlock()
	if ok {
		return img
	}
	img = inspectImage(ctx, ref)
	im.mu.Lock()
	im.seen[ref] = img
	im.mu.Unlock()
	return img
}

func inspectImage(ctx context.Context, ref string) *Image {
	out, err := run(ctx, "", "image", "inspect", ref)
	if err != nil {
		return nil
	}
	var raw []struct {
		ID          string `json:"Id"`
		Created     string
		RepoDigests []string
	}
	if json.Unmarshal(out, &raw) != nil || len(raw) == 0 {
		return nil
	}
	r := raw[0]
	return &Image{
		ID:          r.ID,
		Created:     registry.ParseTime(r.Created),
		RepoDigests: r.RepoDigests,
	}
}

// Project is how to address a compose project from the command line, as
// it was started: its directory, compose files and --env-file files.
type Project struct {
	Name     string
	Dir      string   // working directory
	Files    []string // compose files
	EnvFiles []string // --env-file files, in order
}

// ProjectOf reads a container's compose labels.
func ProjectOf(c Container) Project {
	return Project{
		Name:     c.Project(),
		Dir:      c.Labels[LabelWorkingDir],
		Files:    list(c.Labels[LabelConfig]),
		EnvFiles: list(c.Labels[LabelEnvFiles]),
	}
}

func list(label string) []string {
	var out []string
	for _, f := range strings.Split(label, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Key tells projects apart: the same name started from other files is
// another project.
func (p Project) Key() string {
	return strings.Join([]string{p.Name, p.Dir, strings.Join(p.Files, ","), strings.Join(p.EnvFiles, ",")}, "\x00")
}

// Args are the docker arguments that select the project, as it was
// started: its name, directory, compose files and env files.
func (p Project) Args(sub ...string) []string {
	args := []string{"compose", "-p", p.Name}
	if p.Dir != "" {
		args = append(args, "--project-directory", p.Dir)
	}
	for _, f := range p.Files {
		args = append(args, "-f", f)
	}
	for _, f := range p.EnvFiles {
		args = append(args, "--env-file", f)
	}
	return append(args, sub...)
}

// Service is a service as compose resolves it.
type Service struct {
	Image string
	Build *struct {
		Context    string             `json:"context"`
		Dockerfile string             `json:"dockerfile"`
		Inline     string             `json:"dockerfile_inline"`
		Args       map[string]*string `json:"args"`
		Target     string             `json:"target"`
	}
	Labels map[string]string
}

// Services resolves the project's compose files.
func Services(ctx context.Context, p Project) (map[string]Service, error) {
	out, err := run(ctx, p.Dir, p.Args("config", "--format", "json")...)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Services map[string]Service `json:"services"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return nil, err
	}
	return cfg.Services, nil
}
