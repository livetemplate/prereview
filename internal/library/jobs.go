package library

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Learning jobs: a request to read review stores and propose learnings, handed
// to a background learner (`prereview learn wait`) instead of the review's
// comment queue, so it never lands on the file being edited or interrupts the
// agent doing the edits. A job is a short-lived record in an append-only log;
// its state is the last line for its id (lines are appended under the library
// lock, so file order is time order).
const (
	LearnJobsFile = "learn-jobs.jsonl"
	// LearnerFile is touched by a waiting learner, so a review can tell whether
	// one is running (its mtime, not a process check: that works on Windows too).
	LearnerFile = "learner.json"
	// LearnSeenFile records when you last saw the learnings added for you, for
	// every review on this machine (like BootstrapFile).
	LearnSeenFile = "learn-seen.json"
)

// Job states.
const (
	JobQueued  = "queued"
	JobClaimed = "claimed"
	JobDone    = "done"
)

// JobClaimTTL is how long a claim holds: a learner that died mid-job (its
// session ended) leaves the job claimed, and after this another learner can
// take it.
const JobClaimTTL = 30 * time.Minute

// LearnerStale is how long after a learner's last touch it counts as gone (a
// var so tests can reach the stalled state without waiting it out).
var LearnerStale = 30 * time.Second

// Job is one learning job record.
type Job struct {
	ID         string   `json:"id"`
	Op         string   `json:"op"` // queued | claimed | done
	Stores     []string `json:"stores,omitempty"`
	AutoAccept bool     `json:"auto_accept,omitempty"` // fixed when queued, from the reviewer's pref
	Server     string   `json:"server,omitempty"`      // the store of the review that queued it
	At         string   `json:"at"`                    // when Op happened (a queued job's: when it was queued)
	Machine    string   `json:"machine"`
}

// jobs folds the log into the latest record per id, in first-queued order.
func jobs(dir string) []Job {
	byID := map[string]int{}
	var out []Job
	eachLine(filepath.Join(dir, LearnJobsFile), func(b []byte) {
		var j Job
		if json.Unmarshal(b, &j) != nil || !ValidID(j.ID) {
			return
		}
		if i, ok := byID[j.ID]; ok {
			out[i] = j
			return
		}
		if j.Op != JobQueued {
			return // a claim or finish of a job never queued: torn or foreign
		}
		byID[j.ID] = len(out)
		out = append(out, j)
	})
	return out
}

// claimable reports a job a learner may take now.
func (j Job) claimable(now time.Time) bool {
	switch j.Op {
	case JobQueued:
		return true
	case JobClaimed:
		at, err := time.Parse(time.RFC3339Nano, j.At)
		return err != nil || now.Sub(at) > JobClaimTTL
	}
	return false
}

// QueueJob appends a queued job and returns it. With onlyIfIdle, nothing is
// queued (ok false) when an open job already covers every store asked for.
func QueueJob(dir string, j Job, onlyIfIdle bool) (Job, bool, error) {
	if dir == "" {
		return Job{}, false, errors.New("no library folder")
	}
	if len(j.Stores) == 0 {
		return Job{}, false, errors.New("a learning job needs at least one store")
	}
	unlock, err := lockLibrary(dir)
	if err != nil {
		return Job{}, false, err
	}
	defer unlock()
	if onlyIfIdle && coversAll(OpenJobs(dir), j.Stores) {
		return Job{}, false, nil
	}
	j.ID, j.Op, j.At, j.Machine = "j"+NewID()[1:], JobQueued, Now(), Machine()
	return j, true, Append(filepath.Join(dir, LearnJobsFile), j)
}

func coversAll(open []Job, stores []string) bool {
	for _, s := range stores {
		if !slices.ContainsFunc(open, func(j Job) bool { return slices.Contains(j.Stores, s) }) {
			return false
		}
	}
	return true
}

// ClaimJob takes the oldest claimable job, if any.
func ClaimJob(dir string) (Job, bool, error) {
	unlock, err := lockLibrary(dir)
	if err != nil {
		return Job{}, false, err
	}
	defer unlock()
	now := time.Now()
	for _, j := range jobs(dir) {
		if !j.claimable(now) {
			continue
		}
		j.Op, j.At, j.Machine = JobClaimed, Now(), Machine()
		return j, true, Append(filepath.Join(dir, LearnJobsFile), j)
	}
	return Job{}, false, nil
}

// FinishJob marks a job done.
func FinishJob(dir, id string) error {
	unlock, err := lockLibrary(dir)
	if err != nil {
		return err
	}
	defer unlock()
	j, ok := JobByID(dir, id)
	switch {
	case !ok:
		return fmt.Errorf("learning job %s: not found", id)
	case j.Op == JobDone:
		return nil // finishing twice is harmless
	}
	j.Op, j.At, j.Machine = JobDone, Now(), Machine()
	return Append(filepath.Join(dir, LearnJobsFile), j)
}

// JobByID is a job's current record. It doesn't lock: callers that write
// (FinishJob, Propose) already hold the library lock.
func JobByID(dir, id string) (Job, bool) {
	for _, j := range jobs(dir) {
		if j.ID == id {
			return j, true
		}
	}
	return Job{}, false
}

// OpenJobs is every job not yet done, oldest first.
func OpenJobs(dir string) []Job {
	var out []Job
	for _, j := range jobs(dir) {
		if j.Op != JobDone {
			out = append(out, j)
		}
	}
	return out
}

// TouchLearner records that a learner is waiting now.
func TouchLearner(dir string) error {
	p := filepath.Join(dir, LearnerFile)
	now := time.Now()
	if err := os.Chtimes(p, now, now); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, nil, 0o644) // only its mtime is read
}

// LearnSeenAt is when you last saw the learnings added for you ("" never).
func LearnSeenAt(dir string) string {
	var v struct {
		At string `json:"at"`
	}
	b, err := os.ReadFile(filepath.Join(dir, LearnSeenFile))
	if err != nil || json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.At
}

// MarkLearnSeen records that you've seen them now, and returns that time.
func MarkLearnSeen(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("no library folder")
	}
	unlock, err := lockLibrary(dir)
	if err != nil {
		return "", err
	}
	defer unlock()
	now := Now()
	b, _ := json.Marshal(map[string]string{"at": now, "machine": Machine()})
	return now, WriteFileAtomic(filepath.Join(dir, LearnSeenFile), append(b, '\n'), 0o644)
}

// LearnerAlive reports whether a learner touched the library within LearnerStale.
func LearnerAlive(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, LearnerFile))
	return err == nil && time.Since(fi.ModTime()) < LearnerStale
}
