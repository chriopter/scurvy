// Package registry asks image registries what a tag points to now, without
// pulling anything: the digest behind a tag, when an image was built, and
// which tags exist. It logs in with what `docker login` stored in
// config.json, else anonymously; credential helpers are not used.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Ref is an image reference split into its parts.
type Ref struct {
	Host   string // registry host, e.g. registry-1.docker.io, ghcr.io
	Repo   string // repository on that host, e.g. library/python
	Tag    string // empty when pinned by digest only
	Digest string // sha256:… when pinned by digest
	Name   string // the reference as written, without tag and digest
}

// Parse splits an image reference the way docker does: a first path part
// with a dot, a colon or "localhost" is a registry; otherwise it is Docker
// Hub, where single names live under library/.
func Parse(s string) Ref {
	var r Ref
	name := s
	if i := strings.Index(name, "@"); i >= 0 {
		name, r.Digest = name[:i], name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, r.Tag = name[:i], name[i+1:]
	}
	if r.Tag == "" && r.Digest == "" {
		r.Tag = "latest"
	}
	r.Name = name
	host, repo, ok := strings.Cut(name, "/")
	if !ok || !(strings.ContainsAny(host, ".:") || host == "localhost") {
		host, repo = "docker.io", name
	}
	if host == "docker.io" || host == "index.docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	}
	r.Host, r.Repo = host, repo
	return r
}

// Official reports whether the image is one of Docker Hub's official
// images (python, alpine, debian, …), and its short name.
func (r Ref) Official() (string, bool) {
	if r.Host != "registry-1.docker.io" {
		return "", false
	}
	return strings.CutPrefix(r.Repo, "library/")
}

// maxPages bounds a tag listing; a longer one is reported, not cut short.
const maxPages = 30

var errTooManyTags = errors.New("too many tags to compare")

var manifestTypes = strings.Join([]string{
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
}, ", ")

// Client talks to registries. It remembers tokens and answers for one
// scan, so the same image is asked about once.
type Client struct {
	http *http.Client

	mu      sync.Mutex
	tokens  map[string]string // host/repo → Authorization header
	answers map[string]*answer
}

type answer struct {
	once sync.Once
	val  any
	err  error
}

func New() *Client {
	return &Client{
		http: &http.Client{
			Timeout: 20 * time.Second,
			// Never follow a redirect from https to plain http.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return errors.New("too many redirects")
				}
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing a redirect to %s", req.URL.Scheme)
				}
				return nil
			},
		},
		tokens:  map[string]string{},
		answers: map[string]*answer{},
	}
}

// remember runs fn once per key and hands every caller the same result.
func (c *Client) remember(key string, fn func() (any, error)) (any, error) {
	c.mu.Lock()
	a := c.answers[key]
	if a == nil {
		a = &answer{}
		c.answers[key] = a
	}
	c.mu.Unlock()
	a.once.Do(func() { a.val, a.err = fn() })
	return a.val, a.err
}

// Digest is what the tag points to now: the digest of its index (or of its
// manifest, for single-platform images), as docker records in RepoDigests.
func (c *Client) Digest(ctx context.Context, r Ref) (string, error) {
	if t, err := c.hubTag(ctx, r, r.Tag); err == nil && t.Digest != "" {
		return t.Digest, nil
	}
	v, err := c.remember("digest "+r.Host+"/"+r.Repo+":"+r.Tag, func() (any, error) {
		if resp, err := c.do(ctx, http.MethodHead, r, "/manifests/"+r.Tag, manifestTypes); err == nil {
			resp.Body.Close()
			if d := resp.Header.Get("Docker-Content-Digest"); d != "" {
				return d, nil
			}
		}
		// Some registries refuse HEAD or leave the header out of it.
		resp, err := c.do(ctx, http.MethodGet, r, "/manifests/"+r.Tag, manifestTypes)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if d := resp.Header.Get("Docker-Content-Digest"); d != "" {
			return d, nil
		}
		h := sha256.New()
		if _, err := io.Copy(h, io.LimitReader(resp.Body, 4<<20)); err != nil {
			return "", err
		}
		return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
	})
	s, _ := v.(string)
	return s, err
}

// Created is when the image behind reference (a tag or a digest) was
// built, for this machine's platform: the "created" of its config.
//
// On Docker Hub it is when the tag was last pushed instead, which Docker
// Hub tells without counting a pull; reference is then the tag's current
// digest or a tag.
func (c *Client) Created(ctx context.Context, r Ref, reference string) (time.Time, error) {
	tag := reference
	if strings.HasPrefix(reference, "sha256:") {
		tag = r.Tag
	}
	if t, err := c.hubTag(ctx, r, tag); err == nil && !t.Pushed.IsZero() &&
		(tag == reference || t.Digest == reference) {
		return t.Pushed, nil
	}
	v, err := c.remember("created "+r.Host+"/"+r.Repo+"@"+reference, func() (any, error) {
		var m struct {
			Manifests []struct {
				Digest   string `json:"digest"`
				Platform struct {
					OS           string `json:"os"`
					Architecture string `json:"architecture"`
				} `json:"platform"`
			} `json:"manifests"`
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if err := c.getJSON(ctx, r, "/manifests/"+reference, manifestTypes, &m); err != nil {
			return time.Time{}, err
		}
		if len(m.Manifests) > 0 { // an index: pick this platform's image
			pick := ""
			for _, e := range m.Manifests {
				if e.Platform.OS == "linux" && e.Platform.Architecture == runtime.GOARCH {
					pick = e.Digest
					break
				}
			}
			if pick == "" {
				return time.Time{}, fmt.Errorf("no linux/%s image", runtime.GOARCH)
			}
			m.Manifests = nil
			if err := c.getJSON(ctx, r, "/manifests/"+pick, manifestTypes, &m); err != nil {
				return time.Time{}, err
			}
		}
		if m.Config.Digest == "" {
			return time.Time{}, errors.New("manifest without config")
		}
		var cfg struct {
			Created string `json:"created"`
		}
		if err := c.getJSON(ctx, r, "/blobs/"+m.Config.Digest, "", &cfg); err != nil {
			return time.Time{}, err
		}
		return ParseTime(cfg.Created), nil
	})
	t, _ := v.(time.Time)
	return t, err
}

// Tags lists the repository's tags, following pages. On Docker Hub only
// the tags that contain like (empty: all) are asked for, to spare its limit.
func (c *Client) Tags(ctx context.Context, r Ref, like string) ([]string, error) {
	if tags, err := c.hubTags(ctx, r, like); err == nil {
		return tags, nil
	}
	v, err := c.remember("tags "+r.Host+"/"+r.Repo, func() (any, error) {
		var all []string
		path := "/tags/list?n=1000"
		for page := 0; path != ""; page++ {
			if page == maxPages {
				return nil, errTooManyTags
			}
			resp, err := c.do(ctx, http.MethodGet, r, path, "")
			if err != nil {
				return nil, err
			}
			var list struct {
				Tags []string `json:"tags"`
			}
			err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&list)
			resp.Body.Close()
			if err != nil {
				return nil, err
			}
			all = append(all, list.Tags...)
			path = nextPage(resp.Header.Get("Link"), r.Repo)
		}
		return all, nil
	})
	tags, _ := v.([]string)
	return tags, err
}

var linkNext = regexp.MustCompile(`<([^>]+)>\s*;\s*rel="?next"?`)

// nextPage turns a Link header into the path after /v2/<repo>.
func nextPage(link, repo string) string {
	m := linkNext.FindStringSubmatch(link)
	if m == nil {
		return ""
	}
	u, err := url.Parse(m[1])
	if err != nil {
		return ""
	}
	p, ok := strings.CutPrefix(u.Path, "/v2/"+repo)
	if !ok {
		return ""
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

func (c *Client) getJSON(ctx context.Context, r Ref, path, accept string, v any) error {
	resp, err := c.do(ctx, http.MethodGet, r, path, accept)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// do sends a request to /v2/<repo><path>, fetching an anonymous token
// when the registry asks for one.
func (c *Client) do(ctx context.Context, method string, r Ref, path, accept string) (*http.Response, error) {
	key := r.Host + "/" + r.Repo
	for try := 0; ; try++ {
		req, err := http.NewRequestWithContext(ctx, method, "https://"+r.Host+"/v2/"+r.Repo+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "scurvy")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		c.mu.Lock()
		auth := c.tokens[key]
		c.mu.Unlock()
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && try == 0 {
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			auth, err := c.authorize(ctx, r.Host, challenge, r.Repo)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", loginHost(r.Host), err)
			}
			c.mu.Lock()
			c.tokens[key] = auth
			c.mu.Unlock()
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: rate limited, try again later", loginHost(r.Host))
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: %s", r.Host, resp.Status)
		}
		return resp, nil
	}
}

var challengeParam = regexp.MustCompile(`(\w+)="([^"]*)"`)

// authorize answers a registry's challenge with an Authorization header:
// Basic with the docker login, or a Bearer token fetched from the realm
// it names (over https only), with the login if there is one.
func (c *Client) authorize(ctx context.Context, host, challenge, repo string) (string, error) {
	user, pass, login := credentials(host)
	if _, ok := cutPrefixFold(challenge, "Basic"); ok {
		if !login {
			return "", fmt.Errorf("private image: run docker login %s", loginHost(host))
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass)), nil
	}
	rest, ok := cutPrefixFold(challenge, "Bearer ")
	if !ok {
		return "", errors.New("unknown login challenge")
	}
	params := map[string]string{}
	for _, m := range challengeParam.FindAllStringSubmatch(rest, -1) {
		params[m[1]] = m[2]
	}
	realm, err := url.Parse(params["realm"])
	if err != nil || realm.Host == "" {
		return "", errors.New("token challenge without realm")
	}
	if realm.Scheme != "https" {
		return "", fmt.Errorf("refusing a token realm over %s", realm.Scheme)
	}
	q := realm.Query()
	for k, v := range params {
		if k != "realm" {
			q.Set(k, v)
		}
	}
	if q.Get("scope") == "" {
		q.Set("scope", "repository:"+repo+":pull")
	}
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "scurvy")
	if login {
		req.SetBasicAuth(user, pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if login {
			return "", fmt.Errorf("the login of docker login %s is not let in", loginHost(host))
		}
		return "", fmt.Errorf("private image: run docker login %s", loginHost(host))
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token: %s", resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&t); err != nil {
		return "", err
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	if t.Token == "" {
		return "", errors.New("token: empty")
	}
	return "Bearer " + t.Token, nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// ParseTime reads the timestamps docker and registries write. Images built
// reproducibly claim 1970 (or nothing); that is no date at all, so zero.
func ParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s))
	if err != nil || t.Year() < 2000 {
		return time.Time{}
	}
	return t
}

// credentials finds a docker login for host in docker's config.json, as
// `docker login` writes it (not credential helpers).
func credentials(host string) (user, pass string, ok bool) {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".docker")
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return "", "", false
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return "", "", false
	}
	for _, key := range []string{loginHost(host), "https://" + loginHost(host), "https://index.docker.io/v1/"} {
		if key == "https://index.docker.io/v1/" && host != "registry-1.docker.io" {
			continue
		}
		a, found := cfg.Auths[key]
		if !found || a.Auth == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(a.Auth)
		if err != nil {
			continue
		}
		user, pass, ok = strings.Cut(string(raw), ":")
		if ok {
			return user, pass, true
		}
	}
	return "", "", false
}

// loginHost is the host as docker login names it.
func loginHost(host string) string {
	if host == "registry-1.docker.io" {
		return "docker.io"
	}
	return host
}

// hubTag is what Docker Hub's own API says about a tag: its digest and
// when it was last pushed. It does not count against Docker Hub's pull
// limit, unlike asking the registry for a manifest.
type hubTag struct {
	Digest string
	Pushed time.Time
}

const hubAPI = "https://hub.docker.com/v2/namespaces/"

func hubRepo(r Ref) (namespace, name string, ok bool) {
	if r.Host != "registry-1.docker.io" {
		return "", "", false
	}
	return strings.Cut(r.Repo, "/")
}

func (c *Client) hubTag(ctx context.Context, r Ref, tag string) (hubTag, error) {
	ns, name, ok := hubRepo(r)
	if !ok || tag == "" {
		return hubTag{}, errors.New("not on Docker Hub")
	}
	v, err := c.remember("hub "+r.Repo+":"+tag, func() (any, error) {
		var t struct {
			Digest string `json:"digest"`
			Pushed string `json:"tag_last_pushed"`
		}
		if err := c.hubGet(ctx, hubAPI+ns+"/repositories/"+name+"/tags/"+url.PathEscape(tag), &t); err != nil {
			return hubTag{}, err
		}
		return hubTag{Digest: t.Digest, Pushed: ParseTime(t.Pushed)}, nil
	})
	t, _ := v.(hubTag)
	return t, err
}

func (c *Client) hubTags(ctx context.Context, r Ref, like string) ([]string, error) {
	ns, name, ok := hubRepo(r)
	if !ok {
		return nil, errors.New("not on Docker Hub")
	}
	v, err := c.remember("hubtags "+r.Repo+"~"+like, func() (any, error) {
		next := hubAPI + ns + "/repositories/" + name + "/tags?page_size=100"
		if like != "" {
			next += "&name=" + url.QueryEscape(like)
		}
		var all []string
		for page := 0; next != ""; page++ {
			if page == maxPages {
				return nil, errTooManyTags
			}
			var list struct {
				Next    string `json:"next"`
				Results []struct {
					Name string `json:"name"`
				} `json:"results"`
			}
			if err := c.hubGet(ctx, next, &list); err != nil {
				return nil, err
			}
			for _, t := range list.Results {
				all = append(all, t.Name)
			}
			next = list.Next
		}
		return all, nil
	})
	tags, _ := v.([]string)
	return tags, err
}

func (c *Client) hubGet(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "scurvy")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("hub.docker.com: %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}

// RecentTags lists the tags pushed last: on Docker Hub the newest hundred,
// elsewhere all of them. Enough to see a new major version.
func (c *Client) RecentTags(ctx context.Context, r Ref) ([]string, error) {
	ns, name, ok := hubRepo(r)
	if !ok {
		return c.Tags(ctx, r, "")
	}
	v, err := c.remember("hubrecent "+r.Repo, func() (any, error) {
		var list struct {
			Results []struct {
				Name string `json:"name"`
			} `json:"results"`
		}
		if err := c.hubGet(ctx, hubAPI+ns+"/repositories/"+name+"/tags?page_size=100&ordering=last_updated", &list); err != nil {
			return nil, err
		}
		var out []string
		for _, t := range list.Results {
			out = append(out, t.Name)
		}
		return out, nil
	})
	tags, _ := v.([]string)
	return tags, err
}
