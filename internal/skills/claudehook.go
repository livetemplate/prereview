package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/library"
)

// The Claude Code hook: one PostToolUse entry in ~/.claude/settings.json that
// runs `prereview hook check` after every Write or Edit, so the checks reach the
// agent whether or not it loaded a learned skill. settings.json is yours: only
// that entry is ever added, changed or removed, every other key keeps its value
// and its place, and the previous bytes are kept beside it.

// HookMarker identifies prereview's entry among your hooks.
const HookMarker = "prereview hook check"

// HookCommand is the entry's command. A missing prereview (uninstalled, not on
// PATH) exits 0, so it never fails your edits; `|| true` would also swallow
// exit 2, which is how the findings reach the agent.
const HookCommand = "command -v prereview >/dev/null 2>&1 || exit 0; " + HookMarker

// hookTimeout is the entry's timeout, in seconds.
const hookTimeout = 30

// ClaudeSettings is Claude Code's settings file, relative to the agent home.
const ClaudeSettings = ".claude/settings.json"

const hookKey = "#prereview-hook" // the ownership-record key suffix

// errSettingsShape: settings.json isn't the shape prereview expects; nothing is written.
var errSettingsShape = errors.New("not the JSON shape prereview expects, so it was left alone")

type hookEntry struct {
	Matcher string    `json:"matcher"`
	Hooks   []hookCmd `json:"hooks"`
}

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// member is one key of a JSON object, its value kept as written.
type member struct {
	Key string
	Val json.RawMessage
}

// decodeObject reads a JSON object's members in order (nil for null).
func decodeObject(raw []byte) ([]member, error) {
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errSettingsShape
	}
	var ms []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errSettingsShape
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		ms = append(ms, member{key, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, errSettingsShape // something after the object
	}
	return ms, nil
}

// encodeObject writes members back in order, compact.
func encodeObject(ms []member) []byte {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(marshalNoEscape(m.Key))
		b.WriteByte(':')
		b.Write(m.Val)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// marshalNoEscape is json.Marshal without HTML escaping (the command's `>`
// stays readable), and without the trailing newline.
func marshalNoEscape(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v) // only our own strings and structs
	return bytes.TrimRight(b.Bytes(), "\n")
}

func find(ms []member, key string) int {
	for i, m := range ms {
		if m.Key == key {
			return i
		}
	}
	return -1
}

// hookEntryJSON is the entry prereview writes, compact.
func hookEntryJSON() []byte {
	return marshalNoEscape(hookEntry{Matcher: "Write|Edit|MultiEdit",
		Hooks: []hookCmd{{Type: "command", Command: HookCommand, Timeout: hookTimeout}}})
}

func compact(raw []byte) string {
	var b bytes.Buffer
	if json.Compact(&b, raw) != nil {
		return string(raw)
	}
	return b.String()
}

// isOurs reports whether a PostToolUse entry is prereview's (its command carries
// the marker).
func isOurs(raw json.RawMessage) bool {
	var e hookEntry
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	for _, h := range e.Hooks {
		if strings.Contains(h.Command, HookMarker) {
			return true
		}
	}
	return false
}

// writeHook adds prereview's entry to the settings file at path (on) or removes
// it (!on), and reports whether the file changed. An entry edited since
// prereview wrote it is left (ErrEdited).
func (o *Owner) writeHook(path string, on bool) (bool, error) {
	key := path + hookKey
	cur, err := os.ReadFile(path)
	existed := err == nil
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !on {
			o.record(key, "")
			return false, nil
		}
		cur = []byte("{}")
	case err != nil:
		return false, err
	}
	top, err := decodeObject(cur)
	if err != nil {
		return false, errSettingsShape
	}
	hi := find(top, "hooks")
	var hooks []member
	if hi >= 0 {
		if hooks, err = decodeObject(top[hi].Val); err != nil {
			return false, errSettingsShape
		}
	}
	pi := find(hooks, "PostToolUse")
	var post []json.RawMessage
	if pi >= 0 {
		if err := json.Unmarshal(hooks[pi].Val, &post); err != nil {
			return false, errSettingsShape
		}
	}
	ours := -1
	for i, e := range post {
		if isOurs(e) {
			ours = i
			break
		}
	}
	want := string(hookEntryJSON())
	if ours >= 0 {
		have := compact(post[ours])
		if rec, ok := o.hashes[key]; ok && rec != hash([]byte(have)) && have != want {
			return false, ErrEdited
		}
		if on && have == want {
			o.record(key, want)
			return false, nil
		}
	}
	switch {
	case on && ours >= 0:
		post[ours] = json.RawMessage(want)
	case on:
		post = append(post, json.RawMessage(want))
	case ours < 0:
		o.record(key, "")
		return false, nil
	default:
		post = append(post[:ours], post[ours+1:]...)
	}

	// Put the members back where they were; drop what removing ours emptied.
	set := func(ms []member, i int, k string, v []byte, keep bool) []member {
		switch {
		case !keep && i >= 0:
			return append(ms[:i], ms[i+1:]...)
		case !keep:
			return ms
		case i >= 0:
			ms[i].Val = v
			return ms
		}
		return append(ms, member{k, v})
	}
	var postJSON bytes.Buffer
	postJSON.WriteByte('[')
	for i, e := range post {
		if i > 0 {
			postJSON.WriteByte(',')
		}
		postJSON.Write(e)
	}
	postJSON.WriteByte(']')
	hooks = set(hooks, pi, "PostToolUse", postJSON.Bytes(), len(post) > 0)
	top = set(top, hi, "hooks", encodeObject(hooks), len(hooks) > 0)

	var out bytes.Buffer
	if err := json.Indent(&out, []byte(compact(encodeObject(top))), "", "  "); err != nil {
		return false, fmt.Errorf("rebuilding %s: %w", path, err)
	}
	out.WriteByte('\n')
	if !json.Valid(out.Bytes()) {
		return false, fmt.Errorf("rebuilding %s gave invalid JSON; left alone", path)
	}
	mode := os.FileMode(0o644)
	if existed {
		if fi, err := os.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
		if err := library.WriteFileAtomic(path+".prereview-bak", cur, mode); err != nil {
			return false, err
		}
	}
	if err := library.WriteFileAtomic(path, out.Bytes(), mode); err != nil {
		return false, err
	}
	if on {
		o.record(key, want)
	} else {
		o.record(key, "")
	}
	return true, nil
}

// syncHook adds (on) or removes the hook in Claude Code's settings, when Claude
// Code is set up here (~/.claude exists).
func syncHook(o *Owner, home string, on bool) (*PointerReport, []string, error) {
	path := filepath.Join(home, filepath.FromSlash(ClaudeSettings))
	r := &PointerReport{Label: "Claude Code checks after each edit", Path: path}
	if fi, err := os.Stat(filepath.Dir(path)); err != nil || !fi.IsDir() {
		if !on {
			return nil, nil, nil
		}
		r.State = NotSetUp
		return r, nil, nil
	}
	ch, err := o.writeHook(path, on)
	switch {
	case errors.Is(err, ErrEdited):
		r.State = EditedThere
	case errors.Is(err, errSettingsShape):
		r.State = "settings.json " + errSettingsShape.Error()
	case err != nil:
		return nil, nil, err
	case on:
		r.State = Added
	default:
		r.State = Off
	}
	var changed []string
	if ch {
		changed = []string{path}
	}
	return r, changed, nil
}
