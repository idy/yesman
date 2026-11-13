package pipkg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Keyword marks an npm package as a pi package: pi's package gallery,
// pi.dev/packages, lists the npm packages that carry it.
const Keyword = "pi-package"

// Registry is the npm registry; tests point it at their own server.
var Registry = "https://registry.npmjs.org"

// client bounds every registry request, on top of the caller's context.
var client = &http.Client{Timeout: 30 * time.Second}

// Listing is a pi package as a search lists it.
type Listing struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Description string    `json:"description,omitempty"`
	Publisher   string    `json:"publisher,omitempty"`
	License     string    `json:"license,omitempty"`
	Date        time.Time `json:"date,omitzero"` // when this version was published
	Weekly      int       `json:"weekly"`        // downloads in the last week
	Monthly     int       `json:"monthly"`
	NPM         string    `json:"npm,omitempty"`        // its page on npmjs.com
	Repository  string    `json:"repository,omitempty"` // a browsable URL
	Homepage    string    `json:"homepage,omitempty"`
}

// Results is a page of a search, and how many packages match in all.
type Results struct {
	Total    int       `json:"total"`
	From     int       `json:"from"`
	Packages []Listing `json:"packages"`
}

// SearchSize is how many packages a page of results holds.
const SearchSize = 30

// Search finds pi packages whose name, description or keywords match the
// query, from the from-th one on, in the registry's order, which puts
// popular packages first. An empty query lists them all.
func Search(ctx context.Context, query string, from int) (Results, error) {
	if from < 0 {
		from = 0
	}
	text := "keywords:" + Keyword
	if q := strings.Join(strings.Fields(query), " "); q != "" {
		text += " " + q
	}
	v := url.Values{"text": {text}, "size": {strconv.Itoa(SearchSize)}, "from": {strconv.Itoa(from)}}
	var doc struct {
		Total   int `json:"total"`
		Objects []struct {
			Downloads struct {
				Monthly int `json:"monthly"`
				Weekly  int `json:"weekly"`
			} `json:"downloads"`
			Package struct {
				Name        string    `json:"name"`
				Version     string    `json:"version"`
				Description string    `json:"description"`
				License     string    `json:"license"`
				Date        time.Time `json:"date"`
				Publisher   struct {
					Username string `json:"username"`
				} `json:"publisher"`
				Links struct {
					NPM        string `json:"npm"`
					Homepage   string `json:"homepage"`
					Repository string `json:"repository"`
				} `json:"links"`
			} `json:"package"`
		} `json:"objects"`
	}
	if err := get(ctx, Registry+"/-/v1/search?"+v.Encode(), 8<<20, &doc); err != nil {
		return Results{}, fmt.Errorf("search the npm registry: %w", err)
	}
	r := Results{Total: doc.Total, From: from, Packages: make([]Listing, 0, len(doc.Objects))}
	for _, o := range doc.Objects {
		p := o.Package
		r.Packages = append(r.Packages, Listing{
			Name: p.Name, Version: p.Version, Description: p.Description, License: p.License, Date: p.Date,
			Publisher: p.Publisher.Username, Weekly: o.Downloads.Weekly, Monthly: o.Downloads.Monthly,
			NPM: webURL(p.Links.NPM), Homepage: webURL(p.Links.Homepage), Repository: repoURL(p.Links.Repository),
		})
	}
	return r, nil
}

// Details is what the registry says of a package's latest version.
type Details struct {
	Listing
	Author   string   `json:"author,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
	Versions int      `json:"versions"` // how many versions it has published
	// IsPi says the package carries the pi-package keyword.
	IsPi bool `json:"isPi"`
	// Image and Video are the gallery previews the package names, https
	// URLs only.
	Image string `json:"image,omitempty"`
	Video string `json:"video,omitempty"`
	// Provides names what its manifest declares: extensions, skills,
	// prompts and themes.
	Provides []string `json:"provides,omitempty"`
	Readme   string   `json:"readme,omitempty"`
}

// ErrNotFound is a package the registry does not have.
var ErrNotFound = errors.New("no such package in the npm registry")

// Package reads a package's document from the registry: its latest
// version's manifest and its README.
func Package(ctx context.Context, name string) (Details, error) {
	if !npmNameRE.MatchString(name) {
		return Details{}, fmt.Errorf("%q is not an npm package name", name)
	}
	var doc struct {
		Name     string                     `json:"name"`
		DistTags map[string]string          `json:"dist-tags"`
		Time     map[string]json.RawMessage `json:"time"` // times, and an object once unpublished
		Versions map[string]json.RawMessage `json:"versions"`
		Readme   string                     `json:"readme"`
	}
	// A document carries every version's manifest; the README in it is
	// capped by the registry, but a long history can still run to megabytes.
	if err := get(ctx, Registry+"/"+escapeName(name), 64<<20, &doc); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Details{}, fmt.Errorf("%s: %w", name, err)
		}
		return Details{}, fmt.Errorf("read %s from the npm registry: %w", name, err)
	}
	latest := doc.DistTags["latest"]
	raw, ok := doc.Versions[latest]
	if !ok {
		return Details{}, fmt.Errorf("%s has no latest version", name)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Details{}, fmt.Errorf("read %s@%s: %w", name, latest, err)
	}
	var published time.Time
	_ = json.Unmarshal(doc.Time[latest], &published)
	d := Details{
		Listing: Listing{
			Name: doc.Name, Version: latest, Description: m.Description, License: m.license(),
			Date: published, NPM: "https://www.npmjs.com/package/" + name,
			Repository: repoURL(m.repository()), Homepage: webURL(m.Homepage),
		},
		Author: m.author(), Keywords: m.Keywords, Versions: len(doc.Versions),
		Image: webURL(m.Pi.Image), Video: webURL(m.Pi.Video), Provides: m.Pi.provides(), Readme: doc.Readme,
	}
	if len(m.NPMUser.Name) > 0 {
		d.Publisher = m.NPMUser.Name
	}
	for _, k := range m.Keywords {
		d.IsPi = d.IsPi || k == Keyword
	}
	return d, nil
}

// Latest is the version an npm package's latest tag points at.
func Latest(ctx context.Context, name string) (string, error) {
	if !npmNameRE.MatchString(name) {
		return "", fmt.Errorf("%q is not an npm package name", name)
	}
	var m struct {
		Version string `json:"version"`
	}
	if err := get(ctx, Registry+"/"+escapeName(name)+"/latest", 4<<20, &m); err != nil {
		return "", err
	}
	return m.Version, nil
}

// manifest is the part of a package.json yesman shows.
type manifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version"`
	Description string          `json:"description"`
	Homepage    string          `json:"homepage"`
	Keywords    []string        `json:"keywords"`
	License     json.RawMessage `json:"license"`    // "MIT", or {"type": "MIT"}
	Repository  json.RawMessage `json:"repository"` // a URL, or {"url": …}
	Author      json.RawMessage `json:"author"`     // "Name <mail>", or {"name": …}
	NPMUser     struct {
		Name string `json:"name"`
	} `json:"_npmUser"`
	Pi piManifest `json:"pi"`
}

// piManifest is the pi key of a package.json.
type piManifest struct {
	Extensions json.RawMessage `json:"extensions"`
	Skills     json.RawMessage `json:"skills"`
	Prompts    json.RawMessage `json:"prompts"`
	Themes     json.RawMessage `json:"themes"`
	Image      string          `json:"image"`
	Video      string          `json:"video"`
}

// provides names the resource kinds the manifest lists any of.
func (p piManifest) provides() []string {
	var out []string
	for _, k := range []struct {
		name string
		raw  json.RawMessage
	}{{"extensions", p.Extensions}, {"skills", p.Skills}, {"prompts", p.Prompts}, {"themes", p.Themes}} {
		var list []string
		if json.Unmarshal(k.raw, &list) == nil && len(list) > 0 {
			out = append(out, k.name)
		}
	}
	return out
}

// declared reports whether the manifest lists any resource kind at all,
// in which case pi loads only what it lists.
func (p piManifest) declared() bool {
	for _, raw := range []json.RawMessage{p.Extensions, p.Skills, p.Prompts, p.Themes} {
		if len(raw) > 0 && string(raw) != "null" {
			return true
		}
	}
	return false
}

func (m manifest) license() string {
	var s string
	if json.Unmarshal(m.License, &s) == nil {
		return s
	}
	var o struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(m.License, &o)
	return o.Type
}

func (m manifest) repository() string {
	var s string
	if json.Unmarshal(m.Repository, &s) == nil {
		return s
	}
	var o struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(m.Repository, &o)
	return o.URL
}

func (m manifest) author() string {
	var s string
	if json.Unmarshal(m.Author, &s) == nil {
		// "Name <mail> (url)": the name alone.
		if i := strings.IndexAny(s, "<("); i >= 0 {
			s = s[:i]
		}
		return strings.TrimSpace(s)
	}
	var o struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(m.Author, &o)
	return o.Name
}

// escapeName puts a package name into a registry path: a scoped name's
// slash is escaped.
func escapeName(name string) string { return strings.Replace(name, "/", "%2F", 1) }

// get reads a JSON document of at most limit bytes.
func get(ctx context.Context, u string, limit int64, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the registry answered %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return errors.New("the registry's answer is too large")
	}
	return json.Unmarshal(body, into)
}

// webURL keeps an http or https URL, for the page to open or show, and
// drops anything else.
func webURL(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.String()
}

var gitHostRE = regexp.MustCompile(`^(?:git\+)?(?:https?|git|ssh)://(?:[^@/]+@)?([^/:]+)(?::\d+)?/(.+?)(?:\.git)?/?$`)
var scpRE = regexp.MustCompile(`^(?:git\+)?(?:[^@/]+@)?([A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+):([^/:][^:]*/[^:]+?)(?:\.git)?/?$`)
var shortRE = regexp.MustCompile(`^(github|gitlab|bitbucket):([^/]+/[^/]+?)(?:\.git)?$`)

// repoURL turns a package's repository, git+https://host/x/y.git,
// git@host:x/y or github:x/y, into a page to browse.
func repoURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if m := shortRE.FindStringSubmatch(s); m != nil {
		return "https://" + m[1] + map[string]string{"github": ".com", "gitlab": ".com", "bitbucket": ".org"}[m[1]] + "/" + m[2]
	}
	if m := gitHostRE.FindStringSubmatch(s); m != nil {
		return webURL("https://" + m[1] + "/" + m[2])
	}
	if !strings.Contains(s, "://") {
		if m := scpRE.FindStringSubmatch(s); m != nil {
			return webURL("https://" + m[1] + "/" + m[2])
		}
		// owner/repo is GitHub's.
		if strings.Count(s, "/") == 1 && !strings.HasPrefix(s, ".") {
			return webURL("https://github.com/" + strings.TrimSuffix(s, ".git"))
		}
	}
	return webURL(s)
}
