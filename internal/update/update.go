// Package update keeps scurvy current from its GitHub releases. It installs
// nothing that is not signed with scurvy's release key.
package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// publicKey checks release signatures: the release workflow signs
// SHA256SUMS with the private half (repository secret SCURVY_SIGNING_KEY).
const publicKey = "8sN378v+F3O0Mi6lNf1qql+qevqXyMMf1y9iw+f0VdU="

const repo = "chriopter/scurvy"

// Asset is this platform's release archive.
var Asset = "scurvy-linux-" + runtime.GOARCH + ".tar.gz"

var client = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	TLSHandshakeTimeout:   15 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
}}

// base, when SCURVY_UPDATE_BASE is set, replaces GitHub for tests:
// <base>/latest answers the newest tag, <base>/<tag>/<file> the files.
// Signatures are checked all the same.
func base() string { return os.Getenv("SCURVY_UPDATE_BASE") }

// Release is the newest release and what its notes list.
type Release struct {
	Tag    string
	Points []string
}

// Latest asks GitHub for the newest release.
func Latest(ctx context.Context) (Release, error) {
	if b := base(); b != "" {
		tag, err := fetch(ctx, b+"/latest")
		return Release{Tag: strings.TrimSpace(string(tag))}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("github: %s", resp.Status)
	}
	var rel struct {
		Tag  string `json:"tag_name"`
		Body string `json:"body"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return Release{}, err
	}
	return Release{Tag: rel.Tag, Points: points(rel.Body)}, nil
}

// points are the list items of release notes, without markup.
func points(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		l, ok := strings.CutPrefix(strings.TrimSpace(l), "- ")
		if !ok {
			continue
		}
		l = strings.NewReplacer("**", "", "`", "", "<kbd>", "", "</kbd>", "").Replace(l)
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Newer reports whether tag a is a later vMAJOR.MINOR.PATCH than b. A
// pre-release (v1.2.0-rc1) ranks below its release.
func Newer(a, b string) bool {
	na, pa := parse(a)
	nb, pb := parse(b)
	for i := range na {
		if na[i] != nb[i] {
			return na[i] > nb[i]
		}
	}
	return pa == "" && pb != ""
}

func parse(tag string) (nums [3]int, pre string) {
	v := strings.TrimPrefix(tag, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	for i, part := range strings.SplitN(v, ".", 3) {
		nums[i], _ = strconv.Atoi(part)
	}
	return nums, pre
}

// Managed reports whether this binary is a release that may update
// itself: a dev build never does.
func Managed(version string) bool {
	return strings.HasPrefix(version, "v") && version != "dev"
}

// Install downloads release tag, checks it and puts it where the running
// program is. It returns that path.
func Install(ctx context.Context, tag string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return "", err
	}
	url := "https://github.com/" + repo + "/releases/download/" + tag + "/"
	if b := base(); b != "" {
		url = b + "/" + tag + "/"
	}
	want, err := checksum(ctx, url+"SHA256SUMS", tag)
	if err != nil {
		return "", err
	}
	data, err := fetchBig(ctx, url+Asset)
	if err != nil {
		return "", err
	}
	if got := sha256.Sum256(data); hex.EncodeToString(got[:]) != want {
		return "", errors.New("download does not match its checksum; not installing")
	}
	bin, err := unpack(data, "scurvy")
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(self), ".scurvy-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return self, os.Rename(tmp.Name(), self)
}

// checksum fetches SHA256SUMS and its signature, checks the signature and
// that the list names this version (an older signed release cannot pass as
// a newer one), and returns this platform's archive checksum.
func checksum(ctx context.Context, url, tag string) (string, error) {
	list, err := fetch(ctx, url)
	if err != nil {
		return "", err
	}
	sig, err := fetch(ctx, url+".sig")
	if err != nil {
		return "", err
	}
	if err := Verify(list, sig); err != nil {
		return "", err
	}
	sum, version := "", ""
	sc := bufio.NewScanner(bytes.NewReader(list))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		switch {
		case len(f) == 2 && f[0] == "version":
			version = f[1]
		case len(f) == 2 && strings.TrimPrefix(f[1], "*") == Asset:
			sum = f[0]
		}
	}
	if version != tag {
		return "", fmt.Errorf("release %s is signed as %q; not installing", tag, version)
	}
	if sum == "" {
		return "", fmt.Errorf("release %s has no %s", tag, Asset)
	}
	return sum, nil
}

// Verify checks a base64 Ed25519 signature of data against the release key.
func Verify(data, sig []byte) error {
	pub, _ := base64.StdEncoding.DecodeString(publicKey)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, data, raw) {
		return errors.New("release signature is invalid; not installing")
	}
	return nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	return get(ctx, url, 1<<20)
}

func fetchBig(ctx context.Context, url string) ([]byte, error) {
	return get(ctx, url, 100<<20)
}

func get(ctx context.Context, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: %s", filepath.Base(url), resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// unpack returns the file called name from a .tar.gz.
func unpack(data []byte, name string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("release archive has no %s", name)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return io.ReadAll(io.LimitReader(tr, 100<<20))
		}
	}
}
