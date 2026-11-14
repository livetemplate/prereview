package review

import (
	"html/template"
	"net/url"
	"strconv"
	"strings"
)

// Editors for "Open in editor". Each is a URL scheme the OS hands to the
// installed app; prereview ships no editor of its own. With an SSH host set,
// the link opens the file on this machine over the editor's remote-SSH support,
// which is the case when the browser is on another device (the tailnet use).
const (
	EditorVSCode = "vscode"
	EditorCursor = "cursor"
	EditorZed    = "zed"
	EditorCustom = "custom"
)

// editorSchemes lists the built-in editors in picker order: display label, the
// local link and the remote (SSH) link, with the same {path}/{line}/{host}
// placeholders a custom template takes.
var editorSchemes = []struct{ id, label, local, remote string }{
	{EditorVSCode, "VS Code", "vscode://file{path}:{line}", "vscode://vscode-remote/ssh-remote+{host}{path}:{line}"},
	{EditorCursor, "Cursor", "cursor://file{path}:{line}", "cursor://vscode-remote/ssh-remote+{host}{path}:{line}"},
	{EditorZed, "Zed", "zed://file{path}:{line}", "zed://ssh/{host}{path}:{line}"},
}

// editorScheme finds a built-in editor's link templates.
func editorScheme(id string) (local, remote string, ok bool) {
	for _, e := range editorSchemes {
		if e.id == id {
			return e.local, e.remote, true
		}
	}
	return "", "", false
}

// EditorOption is one entry in the editor picker.
type EditorOption struct {
	ID       string
	Label    string
	Selected bool
}

// EditorOptions lists the picker entries, the chosen one marked.
func (s PrereviewState) EditorOptions() []EditorOption {
	out := []EditorOption{{ID: "", Label: "None (show path)", Selected: s.Editor == ""}}
	for _, e := range editorSchemes {
		out = append(out, EditorOption{ID: e.id, Label: e.label, Selected: s.Editor == e.id})
	}
	return append(out, EditorOption{ID: EditorCustom, Label: "Custom URL…", Selected: s.Editor == EditorCustom})
}

// editorURL builds the "Open in editor" link for an absolute path, or "" when no
// editor is chosen (the UI then shows the path to copy). A custom template takes
// {path}, {line} and {host}.
func editorURL(editor, custom, sshHost, absPath string, line int) template.URL {
	if absPath == "" {
		return ""
	}
	if line < 1 {
		line = 1
	}
	tmpl := custom
	if editor != EditorCustom {
		local, remote, ok := editorScheme(editor)
		if !ok {
			return ""
		}
		tmpl = local
		if sshHost != "" {
			tmpl = remote
		}
	}
	if tmpl == "" {
		return ""
	}
	u := strings.NewReplacer("{path}", (&url.URL{Path: absPath}).EscapedPath(), "{line}", strconv.Itoa(line),
		"{host}", url.PathEscape(sshHost)).Replace(tmpl)
	// Only hand a non-web, non-script scheme to the browser: the custom template is
	// the user's own, but a javascript: URL must never render as a link.
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "javascript", "data", "vbscript":
		return ""
	}
	return template.URL(u) // #nosec: scheme checked above; html/template would otherwise blank it
}
