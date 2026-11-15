// Package library is prereview's per-user learning store (checks M3): the
// learnings proposed from your reviews, the sessions they come from and the
// extractions already run. Every log is append-only JSONL — several prereview
// servers run at once (one per review) and whole-file rewrites would lose each
// other's updates — and a record is the latest line for its id (last writer wins
// by updated_at, ties by machine). It never imports internal/review: the review
// server builds on it, not the other way round.
package library

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/livetemplate/prereview/internal/flock"
)

// Log file names, in Dir().
const (
	SessionsFile    = "sessions.jsonl"
	LearningsFile   = "learnings.jsonl"
	ExtractionsFile = "extractions.jsonl"
)

// Dir is the per-user library directory: $PREREVIEW_LIBRARY_DIR, else
// <user config dir>/prereview (shared with the checks folder and ui-prefs).
func Dir() string {
	if d := os.Getenv("PREREVIEW_LIBRARY_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "prereview")
}

// Learning statuses. Nothing takes effect until you accept.
const (
	StatusProposed   = "proposed"
	StatusAccepted   = "accepted"
	StatusRejected   = "rejected"
	StatusSuperseded = "superseded"
	StatusDeleted    = "deleted"
)

// DefaultScope is the scope a learning gets when none is proposed.
const DefaultScope = "Writing (all documents)"

// Learning is one lesson drawn from your reviews.
type Learning struct {
	ID       string     `json:"id"`
	Text     string     `json:"text"`
	Scope    string     `json:"scope"`
	Globs    []string   `json:"globs,omitempty"`
	Evidence []Evidence `json:"evidence"`
	Produces []string   `json:"produces,omitempty"` // instruction | checker | judge
	// Checker / Judge are the agent-drafted rule YAML and rubric Markdown a learning
	// produces (checks.ValidateDrafted at propose time and again before writing).
	Checker      string `json:"checker,omitempty"`
	Judge        string `json:"judge,omitempty"`
	Replaces     string `json:"replaces,omitempty"` // the older learning this one would supersede
	Status       string `json:"status"`
	SupersededBy string `json:"superseded_by,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
	Machine      string `json:"machine"`
}

// NewestEvidence is the newest evidence timestamp — the order conflicts follow
// (newer wins: you are learning too).
func (l Learning) NewestEvidence() string {
	var n string
	for _, e := range l.Evidence {
		if e.At > n {
			n = e.At
		}
	}
	return n
}

// Session is one prereview launch: where its store is, so the Library and
// extraction can find past reviews.
type Session struct {
	Store     string `json:"store"`
	Repo      string `json:"repo"`
	Target    string `json:"target,omitempty"`
	StartedAt string `json:"started_at"`
	Machine   string `json:"machine"`
}

// Extraction records that a store's signals were read for learnings, so the
// next one only considers what's new since.
type Extraction struct {
	Store   string `json:"store"`
	At      string `json:"at"`
	Signals int    `json:"signals"` // signals in the store when it ran
	Machine string `json:"machine"`
}

// Now is the timestamp format every log uses (sortable as a string).
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Machine names this machine in records, for the last-writer tiebreak.
func Machine() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown"
	}
	return h
}

// ValidID reports an id safe to use in a file name: letters, digits, - and _.
// A record with any other id (a torn line, a crafted import) is never read.
func ValidID(id string) bool { return validID.MatchString(id) }

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// NewID returns a random learning id.
func NewID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "l" + hex.EncodeToString(b)
}

// Append writes one JSON line to path (creating it and its directory). One
// O_APPEND write per record, so concurrent writers never lose each other's lines.
func Append(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// eachLine calls fn for every non-empty line of path; a missing file is empty.
func eachLine(path string, fn func([]byte)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	eachLineIn(f, fn)
}

// eachLineIn calls fn for every non-empty line read from r.
func eachLineIn(r io.Reader, fn func([]byte)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if b := sc.Bytes(); len(b) > 0 {
			fn(b)
		}
	}
}

// newer reports whether a should replace b in a fold: later updated_at wins,
// ties broken by machine name so every machine folds the same way.
func newer(aUpdated, aMachine, bUpdated, bMachine string) bool {
	if aUpdated != bUpdated {
		return aUpdated > bUpdated
	}
	return aMachine > bMachine
}

// foldLearnings folds a learnings log into the latest record per id, deleted
// ones included (a tombstone must survive compaction and export, or an older
// import would resurrect it). Torn lines are skipped; n is the lines read.
func foldLearnings(r io.Reader) (byID map[string]Learning, n int) {
	byID = map[string]Learning{}
	eachLineIn(r, func(b []byte) {
		n++
		var l Learning
		if json.Unmarshal(b, &l) != nil || !ValidID(l.ID) {
			return // an id names files (learned-<id>.yml): never a path
		}
		if cur, ok := byID[l.ID]; ok && !newer(l.UpdatedAt, l.Machine, cur.UpdatedAt, cur.Machine) {
			return
		}
		byID[l.ID] = l
	})
	return byID, n
}

// foldFile is foldLearnings over dir's log (a missing log is empty).
func foldFile(dir string) (map[string]Learning, int) {
	f, err := os.Open(filepath.Join(dir, LearningsFile))
	if err != nil {
		return map[string]Learning{}, 0
	}
	defer f.Close()
	return foldLearnings(f)
}

// LoadLearnings folds learnings.jsonl in dir: the latest record per id, deleted
// ones dropped, newest first.
func LoadLearnings(dir string) []Learning {
	byID, _ := foldFile(dir)
	return sortedLearnings(byID, false)
}

// sortedLearnings lists a fold newest first (tombstones only when keepDeleted).
func sortedLearnings(byID map[string]Learning, keepDeleted bool) []Learning {
	out := make([]Learning, 0, len(byID))
	for _, l := range byID {
		if keepDeleted || l.Status != StatusDeleted {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// writeLearnings writes a fold as a log: one line per learning, tombstones kept.
func writeLearnings(w io.Writer, byID map[string]Learning) error {
	enc := json.NewEncoder(w)
	for _, l := range sortedLearnings(byID, true) {
		if err := enc.Encode(l); err != nil {
			return err
		}
	}
	return nil
}

// lockPath guards every library log write: appends hold it too, so a
// compaction's rename can't drop a line appended to the old file meanwhile.
func lockPath(dir string) string { return filepath.Join(dir, ".lock") }

// lockLibrary takes the library lock.
func lockLibrary(dir string) (func(), error) { return flock.Lock(lockPath(dir)) }

// Lock takes the library lock for a read-modify-write outside this package
// (generated skills record what they wrote under it).
func Lock(dir string) (func(), error) { return lockLibrary(dir) }

// appendLocked appends v to the named log under the library lock.
func appendLocked(dir, name string, v any) error {
	unlock, err := lockLibrary(dir)
	if err != nil {
		return err
	}
	defer unlock()
	return Append(filepath.Join(dir, name), v)
}

// Compact rewrites learnings.jsonl as one line per learning (its folded state,
// tombstones kept), via temp + rename under the library lock — only when the log
// holds more than compactSlack superseded lines, so it's cheap to call on every
// launch. The fold is unchanged.
func Compact(dir string) error {
	unlock, err := lockLibrary(dir)
	if err != nil {
		return err
	}
	defer unlock()
	byID, lines := foldFile(dir)
	if lines-len(byID) <= compactSlack {
		return nil
	}
	var buf bytes.Buffer
	if err := writeLearnings(&buf, byID); err != nil {
		return err
	}
	return WriteFileAtomic(filepath.Join(dir, LearningsFile), buf.Bytes(), 0o644)
}

// WriteFileAtomic replaces path's content via a temp file + rename beside it, so
// a reader never sees it half-written. A file that already exists keeps its mode;
// a new one gets mode.
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// compactSlack is how many superseded lines the log may carry before Compact
// rewrites it.
const compactSlack = 200

// Import merges another machine's export (a learnings log) into dir: each
// record that is newer than what dir holds for its id is appended (its
// updated_at and machine kept, so importing the same file twice appends nothing
// the second time). A learning that would take effect here — accepted, and new
// or different from the one held — arrives *proposed*: an imported file never
// installs a skill line or a check without your accept on this machine. So the
// same record can fold to accepted there and proposed here until you accept it. Returns how many records were taken, and how
// many of those wait for that accept.
func Import(dir string, from io.Reader) (taken, toAccept int, err error) {
	theirs, _ := foldLearnings(from)
	unlock, err := lockLibrary(dir)
	if err != nil {
		return 0, 0, err
	}
	defer unlock()
	have, _ := foldFile(dir)
	path := filepath.Join(dir, LearningsFile)
	for _, l := range theirs {
		cur, ok := have[l.ID]
		if ok && !newer(l.UpdatedAt, l.Machine, cur.UpdatedAt, cur.Machine) {
			continue
		}
		if l.Status == StatusAccepted && !(ok && cur.Status == StatusAccepted && sameEffect(cur, l)) {
			l.Status, l.SupersededBy = StatusProposed, ""
			toAccept++
		}
		if err := Append(path, l); err != nil {
			return taken, toAccept, err
		}
		taken++
	}
	return taken, toAccept, nil
}

// Export writes dir's folded learnings to w as a log (tombstones included, so a
// delete travels).
func Export(dir string, w io.Writer) error {
	byID, _ := foldFile(dir)
	return writeLearnings(w, byID)
}

// LoadSessions reads sessions.jsonl in dir, latest launch per store, newest
// first.
func LoadSessions(dir string) []Session {
	byStore := map[string]Session{}
	eachLine(filepath.Join(dir, SessionsFile), func(b []byte) {
		var s Session
		if json.Unmarshal(b, &s) != nil || s.Store == "" {
			return
		}
		if cur, ok := byStore[s.Store]; !ok || s.StartedAt > cur.StartedAt {
			byStore[s.Store] = s
		}
	})
	out := make([]Session, 0, len(byStore))
	for _, s := range byStore {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt > out[j].StartedAt })
	return out
}

// RecordSession appends a launch.
func RecordSession(dir string, s Session) error {
	s.StartedAt, s.Machine = Now(), Machine()
	return appendLocked(dir, SessionsFile, s)
}

// LastExtractions is the latest extraction per store.
func LastExtractions(dir string) map[string]Extraction {
	out := map[string]Extraction{}
	eachLine(filepath.Join(dir, ExtractionsFile), func(b []byte) {
		var e Extraction
		if json.Unmarshal(b, &e) != nil || e.Store == "" {
			return
		}
		if cur, ok := out[e.Store]; !ok || e.At > cur.At {
			out[e.Store] = e
		}
	})
	return out
}

// RecordExtraction appends that a store was read (with its signal count then).
func RecordExtraction(dir string, store string, signals int) error {
	return appendLocked(dir, ExtractionsFile, Extraction{Store: store, At: Now(), Signals: signals, Machine: Machine()})
}

// sameEffect: two versions of a learning would put the same thing into skills
// and checks.
func sameEffect(a, b Learning) bool {
	return a.Text == b.Text && a.Scope == b.Scope && a.Checker == b.Checker && a.Judge == b.Judge &&
		strings.Join(a.Produces, ",") == strings.Join(b.Produces, ",") && strings.Join(a.Globs, ",") == strings.Join(b.Globs, ",")
}
