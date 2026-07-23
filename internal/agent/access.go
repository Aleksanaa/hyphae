package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// baseReadDirs are the process-global directories every session may read without
// approval, beyond its own working directory — currently the skills directory, so
// the model can load the SKILL.md files advertised in the system prompt. They are
// seeded into each session's grant set as pre-registered readonly permissions.
// Set once at startup by SetReadRoots.
var baseReadDirs []string

// SetReadRoots records the pre-registered readonly directories (the skills dir).
// Relative roots are resolved against the process working directory. Call once at
// startup, before any agent is created.
func SetReadRoots(roots ...string) {
	baseReadDirs = nil
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			abs = filepath.Clean(r)
		}
		baseReadDirs = append(baseReadDirs, abs)
	}
}

// Grant is one access permission. Type is "readonly", "readwrite", or
// "web_fetch"; Scope is a directory path (readonly/readwrite) or "/"-terminated
// URL prefix (web_fetch). Builtin marks a pre-registered permission — the working
// directory and the skills directory — which is always present, never persisted,
// and not user-revocable. Grants from request_access have Builtin=false.
type Grant struct {
	Type    string `json:"type"`
	Scope   string `json:"scope"`
	Builtin bool   `json:"-"`
}

// grantSet is a session's unified permission list: the pre-registered builtins
// (working directory + skills directory) plus the grants added at runtime via
// request_access. It is per-session (a field of Agent). Directory permissions are
// prefix-based (a scope covers itself and everything under it); URL permissions
// cover everything under the "/"-delimited prefix.
type grantSet struct {
	mu     sync.Mutex
	grants []Grant
}

// newGrantSet returns a grant set pre-registered with the readonly builtins: the
// working directory (scope ".", resolved against the live workDir on each check,
// so it follows a workdir change) and each baseReadDirs entry (the skills dir).
func newGrantSet() *grantSet {
	g := &grantSet{grants: []Grant{{Type: "readonly", Scope: ".", Builtin: true}}}
	for _, d := range baseReadDirs {
		g.grants = append(g.grants, Grant{Type: "readonly", Scope: d, Builtin: true})
	}
	return g
}

// list returns the user grants (excluding builtins), in occurrence order — for
// persistence and the palette.
func (g *grantSet) list() []Grant {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []Grant
	for _, gr := range g.grants {
		if !gr.Builtin {
			out = append(out, gr)
		}
	}
	return out
}

// listAll returns every permission, builtins first, for display to the model.
func (g *grantSet) listAll() []Grant {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]Grant(nil), g.grants...)
}

// load replaces the user grants (keeping the builtins) from stored permissions on
// resume. Scopes are stored verbatim — absolute paths, workdir-relative rpaths,
// or "/"-terminated URL prefixes, as produced by grant/grantScope.
func (g *grantSet) load(grants []Grant) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var kept []Grant
	for _, gr := range g.grants {
		if gr.Builtin {
			kept = append(kept, gr)
		}
	}
	for _, gr := range grants {
		gr.Builtin = false
		kept = append(kept, gr)
	}
	g.grants = kept
}

// revoke removes a user grant matching target's type and scope (builtins are not
// revocable). Returns whether one was removed.
func (g *grantSet) revoke(target Grant) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, gr := range g.grants {
		if gr.Builtin || gr.Type != target.Type || gr.Scope != target.Scope {
			continue
		}
		g.grants = append(g.grants[:i], g.grants[i+1:]...)
		return true
	}
	return false
}

// allowRead reports whether path may be read: it must fall within some readonly
// or readwrite permission (builtin or granted). Directory scopes are resolved
// against workDir, so workdir-relative grants and the "." working-directory
// builtin track the live working directory.
func (g *grantSet) allowRead(path, workDir string) bool {
	if g == nil {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, gr := range g.grants {
		if (gr.Type == "readonly" || gr.Type == "readwrite") && pathWithin(path, resolveScope(gr.Scope, workDir)) {
			return true
		}
	}
	return false
}

// allowWrite reports whether path falls inside a readwrite permission, in which
// case write_file/edit_file may proceed without per-call approval.
func (g *grantSet) allowWrite(path, workDir string) bool {
	if g == nil {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, gr := range g.grants {
		if gr.Type == "readwrite" && pathWithin(path, resolveScope(gr.Scope, workDir)) {
			return true
		}
	}
	return false
}

// allowFetch reports whether rawURL falls under a web_fetch permission, in which
// case web_fetch may proceed without per-call approval.
func (g *grantSet) allowFetch(rawURL string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, gr := range g.grants {
		if gr.Type == "web_fetch" && urlUnder(rawURL, gr.Scope) {
			return true
		}
	}
	return false
}

// grant records an approved request_access grant and returns the normalized scope
// stored (for display). kind is "readonly", "readwrite", or "web_fetch".
func (g *grantSet) grant(kind, target, workDir string) string {
	scope := grantScope(kind, target, workDir)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.grants = append(g.grants, Grant{Type: kind, Scope: scope})
	return scope
}

// grantScope returns the normalized scope string for a request, for display in
// the approval prompt, without recording it.
func grantScope(kind, target, workDir string) string {
	if kind == "web_fetch" {
		return normalizeURLPrefix(target)
	}
	return dirScope(target, workDir)
}

// PermissionsLabel renders a session's current permissions (builtins + grants) as
// a system-reminder for the user message, so the model always knows what it can
// access without approval. Directory scopes are shown resolved against workDir.
// Empty input yields "".
func PermissionsLabel(grants []Grant, workDir string) string {
	if len(grants) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Your current permissions — a call within any of these runs without approval or a reasoning= field; anything else needs the user's approval:")
	for _, g := range grants {
		scope := g.Scope
		if g.Type != "web_fetch" {
			scope = resolveScope(g.Scope, workDir)
		}
		fmt.Fprintf(&b, "\n  - %s: %s", g.Type, scope)
	}
	return SystemReminder(b.String())
}

// PermissionRevokedReminder tells the model the user has revoked a standing
// permission, so it stops relying on it. One-shot, via Session.AddReminder.
func PermissionRevokedReminder(gtype, scope string) string {
	return SystemReminder(fmt.Sprintf(
		"The user revoked your %s permission for %s. You no longer have standing access there; using it again will require approval.",
		gtype, scope))
}

// pathWithin reports whether path is root or lies inside it, comparing cleaned
// lexical paths.
func pathWithin(path, root string) bool {
	if root == "" {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// urlUnder reports whether rawURL falls under prefix, which ends with "/". The
// bare form of the prefix (without the trailing slash) also matches.
func urlUnder(rawURL, prefix string) bool {
	return strings.HasPrefix(rawURL, prefix) || rawURL == strings.TrimSuffix(prefix, "/")
}

// dirScope normalizes a directory request target into its stored scope form:
//   - a home path ("~", "~/…") is expanded and stored absolute (hpath);
//   - an absolute path is stored absolute;
//   - a relative path (rpath) is stored relative to the working directory when it
//     stays inside it, and absolute when it escapes (so it can't silently shift
//     if the session later runs elsewhere).
//
// Relative scopes are re-resolved against the working directory at check time by
// resolveScope.
func dirScope(target, workDir string) string {
	if target == "~" || strings.HasPrefix(target, "~/") {
		return expandHome(target)
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target)
	}
	abs := filepath.Join(workDir, target)
	if pathWithin(abs, workDir) {
		if rel, err := filepath.Rel(workDir, abs); err == nil {
			return rel
		}
	}
	return abs
}

// searchGlobRoot resolves a search_files path_glob to the absolute directory the
// walker roots at and whose read permission gates the call. A bare glob (no "/")
// searches the working directory; otherwise the literal prefix before the first
// wildcard segment is the root (home- and workdir-resolved). A glob with no
// wildcards at all falls back to the parent of the named path.
func searchGlobRoot(pathGlob, workDir string) string {
	if pathGlob == "" || !strings.Contains(pathGlob, "/") {
		return workDir
	}
	abs := filepath.ToSlash(resolvePath(pathGlob, workDir))
	var lit []string
	wild := false
	for _, seg := range strings.Split(abs, "/") {
		if strings.ContainsAny(seg, "*?[") {
			wild = true
			break
		}
		lit = append(lit, seg)
	}
	root := strings.Join(lit, "/")
	if !wild {
		root = filepath.Dir(root)
	}
	if root == "" {
		root = "/"
	}
	return filepath.Clean(root)
}

// resolveScope turns a stored scope back into an absolute path. Relative scopes
// (rpath grants inside the working directory) are joined against workDir; scopes
// are already "~"-free (dirScope expands home eagerly).
func resolveScope(scope, workDir string) string {
	if filepath.IsAbs(scope) {
		return scope
	}
	return filepath.Join(workDir, scope)
}

// expandHome resolves "~" and "~/…" against the current user's home directory,
// returning a cleaned absolute path. Non-home inputs are returned cleaned.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return filepath.Clean(home)
			}
			return filepath.Clean(filepath.Join(home, p[2:]))
		}
	}
	return filepath.Clean(p)
}

// normalizeURLPrefix turns a URL request target into a "/"-terminated prefix, so
// prefix matching stays on path-segment boundaries.
func normalizeURLPrefix(rawURL string) string {
	return strings.TrimRight(rawURL, "/") + "/"
}

// approvalNeeded reports whether a tool call must pause for the user to approve
// it. A call runs immediately (no approval, no reasoning) when it is already
// within the session's permissions: reads inside the working directory, the
// skills directory, or a readonly/readwrite grant; writes inside a readwrite
// grant; fetches under a granted URL prefix. Everything else — reads elsewhere,
// ungranted writes and fetches, and every run_shell/web_search — needs approval.
func approvalNeeded(toolName string, argsMap map[string]any, workDir string, g *grantSet) bool {
	switch toolName {
	case "read_file", "list_directory":
		return !g.allowRead(resolvePath(str(argsMap, "path"), workDir), workDir)
	case "search_files":
		return !g.allowRead(searchGlobRoot(str(argsMap, "path_glob"), workDir), workDir)
	case "write_file", "edit_file":
		return !g.allowWrite(resolvePath(str(argsMap, "path"), workDir), workDir)
	case "web_fetch":
		return !g.allowFetch(str(argsMap, "url"))
	}
	return true
}
