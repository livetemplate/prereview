package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/livetemplate/prereview/internal/skills"
)

// syncInstalledSkills keeps every agent integration that is already installed
// in step with this binary (#208). It runs on every launch, after any
// self-update, so the new binary's embedded text is what lands; that covers
// brew, Scoop and `go install` upgrades too.
//
// A target is installed when at least one of its files exists; one that isn't
// is never touched, so nobody gets an integration they didn't ask for. For an
// installed target, a missing file is recreated only when its own folder
// exists (Claude's reference.md beside SKILL.md). Writes go through the
// ownership record, so a file edited since prereview wrote it is left and
// reported. The result lists only targets that were refreshed or couldn't be.
func syncInstalledSkills(home, lib string) []skills.ClientSync {
	var installed []clientTarget
	for _, t := range clientTargets() {
		for _, f := range t.files {
			if _, err := os.Stat(clientPath(home, f)); err == nil {
				installed = append(installed, t)
				break
			}
		}
	}
	if len(installed) == 0 {
		return nil
	}
	var out []skills.ClientSync
	err := skills.Own(lib, func(o *skills.Owner) error {
		for _, t := range installed {
			if r := syncTarget(o, home, t); r.State != "" {
				out = append(out, r)
			}
		}
		return nil
	})
	if err != nil {
		return failAll(installed, err)
	}
	return out
}

// syncTarget refreshes one installed target's files. Its row carries the worst
// file outcome (skills.SyncRank) and the files that had it; State "" when every
// file was already current.
func syncTarget(o *skills.Owner, home string, t clientTarget) skills.ClientSync {
	r := skills.ClientSync{Client: t.id, Name: t.name()}
	for _, f := range t.files {
		path := clientPath(home, f)
		if _, err := os.Stat(filepath.Dir(path)); err != nil {
			continue // that copy's folder is gone: don't recreate it
		}
		changed, err := o.Update(path, []byte(f.body), f.fileMode())
		if errors.Is(err, skills.ErrForeign) {
			// Installs made before the ownership record have no entry. The path
			// is prereview's own name for its integration, so take it over once.
			changed, err = adopt(o, path, f)
		}
		state, msg := "", ""
		switch {
		case errors.Is(err, skills.ErrEdited):
			state = skills.EditedThere
		case err != nil:
			state, msg = skills.Failed, err.Error()
		case changed:
			state = skills.Refreshed
		default:
			continue
		}
		if skills.SyncRank(state) > skills.SyncRank(r.State) {
			r.State, r.Err, r.Paths = state, msg, nil
		}
		if state == r.State {
			r.Paths = append(r.Paths, path)
		}
	}
	return r
}

// adopt records an unrecorded install file as prereview's, writing this
// binary's copy; it reports whether the bytes changed.
func adopt(o *skills.Owner, path string, f clientFile) (bool, error) {
	cur, _ := os.ReadFile(path)
	return string(cur) != f.body, o.Put(path, []byte(f.body), f.fileMode())
}

// failAll reports every installed target as failed with err.
func failAll(ts []clientTarget, err error) []skills.ClientSync {
	out := make([]skills.ClientSync, 0, len(ts))
	for _, t := range ts {
		out = append(out, skills.ClientSync{Client: t.id, Name: t.name(), State: skills.Failed, Err: err.Error()})
	}
	return out
}

// reportSkillSyncs writes the sync results for whoever reads stderr: which
// agents' files were refreshed (reload the skill there; an agent already
// running holds the old copy), and which were left or failed, with the command
// that fixes them. Nothing when every installed integration was current.
func reportSkillSyncs(w io.Writer, rs []skills.ClientSync) {
	for _, r := range rs {
		paths := strings.Join(r.Paths, ", ")
		if r.State == skills.Refreshed {
			fmt.Fprintf(w, "prereview: refreshed the %s integration to match this binary (%s). Re-read it before continuing, and reload it in %s.\n", r.Name, paths, r.Name)
			continue
		}
		if paths != "" {
			paths = " (" + paths + ")"
		}
		fmt.Fprintf(w, "prereview: the %s integration was not updated%s: %s. Run `%s` to replace it with this binary's copy.\n", r.Name, paths, r.Status(), r.Fix())
	}
}
