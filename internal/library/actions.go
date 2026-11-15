package library

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/livetemplate/prereview/internal/checks"
)

// Produces values a learning may ask for (they act in M4).
var ProducesKinds = []string{"instruction", "checker", "judge"}

// Proposal is what `prereview learn propose` (the agent) submits.
type Proposal struct {
	Text     string
	Scope    string
	Globs    []string
	Evidence []string // "<store>:<id>" refs, each resolved before anything is written
	Replaces string
	Produces []string
	Checker  string // a rule YAML the learning produces (checks.ValidateDrafted)
	Judge    string // a rubric Markdown the learning produces
}

// Propose validates p, resolves every evidence ref and appends a proposed
// learning. Nothing is written when any ref fails to resolve: a learning can
// never cite nothing.
func Propose(dir string, p Proposal) (Learning, error) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return Learning{}, errors.New("learn propose: --text is required")
	}
	if len(p.Evidence) == 0 {
		return Learning{}, errors.New("learn propose: at least one --evidence <store>:<id> is required")
	}
	for _, k := range p.Produces {
		if !slices.Contains(ProducesKinds, k) {
			return Learning{}, fmt.Errorf("learn propose: --produces %q: want one of %s", k, strings.Join(ProducesKinds, ", "))
		}
	}
	for _, d := range []struct {
		body  string
		judge bool
		kind  string
	}{{p.Checker, false, "checker"}, {p.Judge, true, "judge"}} {
		if strings.TrimSpace(d.body) == "" {
			continue
		}
		if err := checks.ValidateDrafted([]byte(d.body), d.judge); err != nil {
			return Learning{}, fmt.Errorf("learn propose: %w", err)
		}
		if !slices.Contains(p.Produces, d.kind) {
			p.Produces = append(p.Produces, d.kind)
		}
	}
	ev := make([]Evidence, 0, len(p.Evidence))
	for _, ref := range p.Evidence {
		e, err := Resolve(ref)
		if err != nil {
			return Learning{}, err
		}
		ev = append(ev, e)
	}
	scope := strings.TrimSpace(p.Scope)
	if scope == "" {
		scope = DefaultScope
	}
	l := Learning{ID: NewID(), Text: text, Scope: scope, Globs: p.Globs, Evidence: ev,
		Produces: p.Produces, Replaces: p.Replaces, Status: StatusProposed,
		Checker: strings.TrimSpace(p.Checker) + nl(p.Checker), Judge: strings.TrimSpace(p.Judge) + nl(p.Judge)}
	err := update(dir, func(have map[string]Learning) ([]Learning, error) {
		if p.Replaces != "" {
			if old, ok := have[p.Replaces]; !ok || old.Status == StatusDeleted {
				return nil, fmt.Errorf("learn propose: --replaces %s: no such learning", p.Replaces)
			}
		}
		return []Learning{l}, nil
	})
	return l, err
}

// Card actions.
const (
	ActAccept  = "accept"
	ActReject  = "reject"
	ActDelete  = "delete"
	ActRestore = "restore"
)

// Act applies a card action to learning id:
//   - accept: proposed/rejected → accepted; when it replaces an accepted
//     learning, that one becomes superseded by it (newer wins);
//   - reject: → rejected;
//   - delete: → deleted (a tombstone; the card goes);
//   - restore: superseded → accepted, and whatever superseded it is
//     superseded in turn, so one learning of the pair is in effect.
func Act(dir, id, action string) error {
	return update(dir, func(have map[string]Learning) ([]Learning, error) {
		l, ok := have[id]
		if !ok || l.Status == StatusDeleted {
			return nil, fmt.Errorf("learning %s: not found", id)
		}
		switch action {
		case ActAccept:
			l.Status, l.SupersededBy = StatusAccepted, ""
			out := []Learning{l}
			if old, ok := have[l.Replaces]; ok && old.Status == StatusAccepted {
				old.Status, old.SupersededBy = StatusSuperseded, l.ID
				out = append(out, old)
			}
			return out, nil
		case ActReject:
			l.Status = StatusRejected
		case ActDelete:
			l.Status = StatusDeleted
		case ActRestore:
			if l.Status != StatusSuperseded {
				return nil, fmt.Errorf("learning %s: only a superseded learning can be restored", id)
			}
			out := []Learning{}
			if by, ok := have[l.SupersededBy]; ok && by.Status == StatusAccepted {
				by.Status, by.SupersededBy = StatusSuperseded, l.ID
				out = append(out, by)
			}
			l.Status, l.SupersededBy = StatusAccepted, ""
			return append(out, l), nil
		default:
			return nil, fmt.Errorf("unknown learning action %q", action)
		}
		return []Learning{l}, nil
	})
}

// Edit changes a learning's text and scope (from the card).
func Edit(dir, id, text, scope string) error {
	text, scope = strings.TrimSpace(text), strings.TrimSpace(scope)
	if text == "" {
		return errors.New("a learning needs text")
	}
	if scope == "" {
		scope = DefaultScope
	}
	return update(dir, func(have map[string]Learning) ([]Learning, error) {
		l, ok := have[id]
		if !ok || l.Status == StatusDeleted {
			return nil, fmt.Errorf("learning %s: not found", id)
		}
		l.Text, l.Scope = text, scope
		return []Learning{l}, nil
	})
}

// update is the library's read-modify-write: under the lock it folds the log,
// lets fn decide the new states, and appends them (stamped).
func update(dir string, fn func(map[string]Learning) ([]Learning, error)) error {
	unlock, err := lockLibrary(dir)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(dir, LearningsFile)
	have, _ := foldFile(dir)
	out, err := fn(have)
	if err != nil {
		return err
	}
	for _, l := range out {
		l.UpdatedAt, l.Machine = Now(), Machine()
		if l.CreatedAt == "" {
			l.CreatedAt = l.UpdatedAt
		}
		if err := Append(path, l); err != nil {
			return err
		}
	}
	return nil
}

// nl ends a non-empty drafted file with one newline.
func nl(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "\n"
}
