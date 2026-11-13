// Package pipkg knows pi packages: the sources pi installs them from, the
// npm registry where pi's package gallery finds them, and what `pi list`
// says is installed. Installing, updating and removing are pi's own
// commands; this package only reads.
package pipkg

import (
	"fmt"
	"regexp"
	"strings"
)

// Error is why a source was refused: an English message with at most one
// %q in it, kept apart from its argument so that the app can say it in
// the user's language.
type Error struct {
	Format string
	Arg    string
}

func (e *Error) Error() string {
	if strings.Contains(e.Format, "%") {
		return fmt.Sprintf(e.Format, e.Arg)
	}
	return e.Format
}

func refuse(format string, arg ...string) error {
	e := &Error{Format: format}
	if len(arg) > 0 {
		e.Arg = arg[0]
	}
	return e
}

// Source is a package source as `pi install` takes it.
type Source struct {
	Raw  string `json:"raw"`
	Kind string `json:"kind"` // npm, git or local
	// Name is an npm package's name, without the version.
	Name string `json:"name,omitempty"`
	// Version is the version or range an npm source names, if any.
	Version string `json:"version,omitempty"`
}

var (
	// npmNameRE is a valid npm package name, scoped or not.
	npmNameRE = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._~-]*/)?[a-z0-9][a-z0-9._~-]*$`)
	// exactRE is an exact version, which pi never moves on update.
	exactRE = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.+-]+)?$`)
	// driveRE starts a Windows path.
	driveRE = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// ParseSource reads npm:name[@version], git:host/path[@ref], an https,
// http or ssh URL, git@host:path, or a local path (absolute, ./, ../ or ~).
// Anything else, and anything that would read as an option, is refused, so
// a source always reaches pi as a source.
func ParseSource(input string) (Source, error) {
	in := strings.TrimSpace(input)
	s := Source{Raw: in}
	lower := strings.ToLower(in)
	switch {
	case in == "":
		return s, refuse("enter a package source, such as npm:pi-web-access")
	case strings.HasPrefix(in, "-"):
		return s, refuse("%q is not a package source", in)
	case strings.ContainsAny(in, "\r\n\x00"):
		return s, refuse("a package source is one line")
	case strings.HasPrefix(lower, "npm:"):
		s.Kind = "npm"
		spec := in[len("npm:"):]
		// A scope's @ comes first; a version's @ comes after the name.
		at := strings.LastIndex(spec, "@")
		if at > 0 {
			s.Name, s.Version = spec[:at], spec[at+1:]
			if s.Version == "" {
				return s, refuse("%q names no version after @", in)
			}
		} else {
			s.Name = spec
		}
		if !npmNameRE.MatchString(s.Name) || len(s.Name) > 214 {
			return s, refuse("%q is not an npm package name", s.Name)
		}
		if strings.ContainsAny(s.Version, " \t") {
			return s, refuse("%q is not a version", s.Version)
		}
	case strings.HasPrefix(lower, "git:"), strings.HasPrefix(lower, "git@"),
		strings.HasPrefix(lower, "https://"), strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "ssh://"):
		s.Kind = "git"
		if strings.ContainsAny(in, " \t") {
			return s, refuse("%q is not a git source", in)
		}
	case strings.HasPrefix(in, "/"), strings.HasPrefix(in, "./"), strings.HasPrefix(in, "../"),
		strings.HasPrefix(in, "~"), strings.HasPrefix(in, `.\`), strings.HasPrefix(in, `..\`), driveRE.MatchString(in):
		s.Kind = "local"
	default:
		return s, refuse("%q is not a package source: use npm:name, git:host/path, a URL or a local path", in)
	}
	return s, nil
}

// Pinned reports whether an npm source names an exact version, which pi
// keeps on update.
func (s Source) Pinned() bool { return s.Kind == "npm" && exactRE.MatchString(s.Version) }
