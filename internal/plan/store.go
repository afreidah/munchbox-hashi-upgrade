// -------------------------------------------------------------------------------
// Run Storage - Create, Open, Save
//
// Author: Alex Freidah
//
// Getting a run on and off disk. A run is saved after every task, so the write
// path is the common one rather than the exceptional one: it stages a sibling
// file, forces it to storage, and renames over the target, which keeps the
// previous run readable if the machine stops partway through.
// -------------------------------------------------------------------------------

package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// ErrSchema is returned for a run file this build cannot interpret.
var ErrSchema = errors.New("unrecognised run schema")

// Runs record cluster layout, so neither the file nor a directory created for
// it is readable beyond the operator.
const (
	fileMode = 0o600
	dirMode  = 0o700
)

// Create builds a run bound to path. It does not touch the filesystem; Save
// does that, so a caller can assemble and inspect a run before committing to
// writing one.
func Create(path, builtBy string, spec Spec, cluster Cluster, tasks []Task) *Run {
	return &Run{
		path:      path,
		Schema:    Schema,
		Built:     builtBy,
		CreatedAt: time.Now().UTC(),
		Spec:      spec,
		Cluster:   cluster,
		Tasks:     tasks,
		Progress:  make(map[string]Record),
	}
}

// Open reads a run and binds it to the file it came from, so a later Save
// returns it to the same place.
func Open(path string) (*Run, error) {
	//nolint:gosec // the path is the operator's own argument; reading what they name is the point
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read run: %w", err)
	}

	var r Run
	if err := yaml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse run: %w", err)
	}
	if r.Schema != Schema {
		return nil, fmt.Errorf("%w: file says %d, this build writes %d", ErrSchema, r.Schema, Schema)
	}
	if r.Progress == nil {
		r.Progress = make(map[string]Record)
	}
	r.path = path
	return &r, nil
}

// Save writes the run back to its own file, replacing it in one step.
func (r *Run) Save() error {
	if r.path == "" {
		return errors.New("run has no path; build it with Create or Open")
	}

	data, err := yaml.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode run: %w", err)
	}

	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}

	// Staged in the destination directory because a rename only replaces a
	// file atomically within one filesystem.
	staged, err := os.CreateTemp(dir, "."+filepath.Base(r.path)+".*")
	if err != nil {
		return fmt.Errorf("stage run: %w", err)
	}
	stagedPath := staged.Name()

	if err := commit(staged, data); err != nil {
		_ = os.Remove(stagedPath)
		return err
	}
	if err := os.Chmod(stagedPath, fileMode); err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("restrict run permissions: %w", err)
	}
	if err := os.Rename(stagedPath, r.path); err != nil {
		_ = os.Remove(stagedPath)
		return fmt.Errorf("replace run: %w", err)
	}
	return nil
}

// commit writes data to f and closes it, forcing the bytes to storage first.
// Without that the rename can land before the contents do, leaving an empty
// run behind in precisely the case the staging exists to survive.
func commit(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write run: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("flush run: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close run: %w", err)
	}
	return nil
}
