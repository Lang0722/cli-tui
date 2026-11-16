package tui

import (
	"net/url"
	"regexp"
	"strings"
)

// Local file links, after Codex markdown_render/local_links.rs: a link to
// a path keeps its label and shows its target, made relative to the
// session's working directory when it is under it, with "#L12C3"
// locations written ":12:3".

var (
	reColonLocation = regexp.MustCompile(`:\d+(?::\d+)?(?:[-–]\d+(?::\d+)?)?$`)
	reHashLocation  = regexp.MustCompile(`^L(\d+)(?:C(\d+))?(?:-L(\d+)(?:C(\d+))?)?$`)
)

// isLocalLink reports whether a link destination is a file path.
func isLocalLink(u string) bool {
	if strings.HasPrefix(u, "file://") || strings.HasPrefix(u, "/") || strings.HasPrefix(u, "~/") ||
		strings.HasPrefix(u, "./") || strings.HasPrefix(u, "../") || strings.HasPrefix(u, `\\`) {
		return true
	}
	return len(u) >= 3 && isASCIILetter(u[0]) && u[1] == ':' && (u[2] == '/' || u[2] == '\\')
}

func isASCIILetter(b byte) bool { return b|0x20 >= 'a' && b|0x20 <= 'z' }

// localLinkTarget is how a local link's destination is shown.
func localLinkTarget(dest, cwd string) string {
	path, loc, ok := parseLocalLink(dest)
	if !ok {
		return dest
	}
	return displayLocalPath(path, cwd) + loc
}

// parseLocalLink splits a destination into its path and a ":line:col"
// location suffix.
func parseLocalLink(dest string) (path, loc string, ok bool) {
	if strings.HasPrefix(dest, "file://") {
		u, err := url.Parse(dest)
		if err != nil {
			return "", "", false
		}
		p := u.Path
		switch {
		case u.Host != "" && u.Host != "localhost":
			p = "//" + u.Host + u.EscapedPath() // a UNC path keeps its escapes (Codex)
		case len(p) >= 3 && p[0] == '/' && isASCIILetter(p[1]) && p[2] == ':':
			p = p[1:] // file:///C:/x
		}
		return normalizeLocalPath(p), hashLocation(u.Fragment), true
	}
	path = dest
	if k := strings.LastIndexByte(dest, '#'); k >= 0 {
		if l := hashLocation(dest[k+1:]); l != "" {
			path, loc = dest[:k], l
		}
	}
	if loc == "" {
		if m := reColonLocation.FindStringIndex(path); m != nil {
			path, loc = path[:m[0]], path[m[0]:]
		}
	}
	if d, err := url.PathUnescape(path); err == nil {
		path = d
	}
	return normalizeLocalPath(path), loc, true
}

// hashLocation turns "L12C3-L14C9" into ":12:3-14:9" ("" if it isn't a
// location).
func hashLocation(frag string) string {
	m := reHashLocation.FindStringSubmatch(frag)
	if m == nil {
		return ""
	}
	s := ":" + m[1]
	if m[2] != "" {
		s += ":" + m[2]
	}
	if m[3] != "" {
		s += "-" + m[3]
		if m[4] != "" {
			s += ":" + m[4]
		}
	}
	return s
}

// normalizeLocalPath writes separators as "/" (\\server\share is
// //server/share).
func normalizeLocalPath(p string) string {
	if rest, ok := strings.CutPrefix(p, `\\`); ok {
		return "//" + strings.TrimLeft(strings.ReplaceAll(rest, `\`, "/"), "/")
	}
	return strings.ReplaceAll(p, `\`, "/")
}

func isAbsLocalPath(p string) bool {
	return strings.HasPrefix(p, "/") || len(p) >= 3 && isASCIILetter(p[0]) && p[1] == ':' && p[2] == '/'
}

func trimLocalSep(p string) string {
	if p == "/" || p == "//" || len(p) == 3 && isASCIILetter(p[0]) && p[1] == ':' && p[2] == '/' {
		return p
	}
	return strings.TrimRight(p, "/")
}

// displayLocalPath shows an absolute path under cwd relative to it; other
// paths (relative ones, "~/...") as they are.
func displayLocalPath(p, cwd string) string {
	if !isAbsLocalPath(p) || cwd == "" {
		return p
	}
	p, c := trimLocalSep(p), trimLocalSep(normalizeLocalPath(cwd))
	if p == c {
		return p
	}
	if c == "/" || c == "//" {
		return strings.TrimPrefix(p, "/")
	}
	if rest, ok := strings.CutPrefix(p, c); ok && strings.HasPrefix(rest, "/") {
		return rest[1:]
	}
	return p
}
