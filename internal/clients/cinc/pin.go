// -------------------------------------------------------------------------------
// Version Pin - Read and Upsert
//
// Author: Alex Freidah
//
// The pin itself. One data bag, one item per tool, one field on the item.
//
// The shape is the cookbook's rather than this package's: the library that
// reads the pin during a converge is the thing that has to find it, so a second
// opinion here would be a second place for it to be wrong.
// -------------------------------------------------------------------------------

package cinc

import (
	"context"
	"errors"
	"fmt"

	cinc "github.com/cinc-project/cinc-api"
)

const (
	versionsBag  = "versions"
	versionField = "version"
	itemIDField  = "id"
)

// Pin returns the version tool is pinned to, or empty when it is pinned to
// nothing.
//
// A bag or item that does not exist is not an error. A tool nobody has pinned
// yet is an ordinary state -- it is what a first upgrade starts from -- and a
// run that refused to plan against it would be refusing the case it exists for.
//
// An item that exists while carrying no version is the opposite: nothing reads
// it and a converge against it fails, so it is reported rather than flattened
// into the same empty string as an absent one.
func (c *Cinc) Pin(ctx context.Context, tool string) (string, error) {
	if tool == "" {
		return "", errors.New("tool is required")
	}

	item, _, err := c.client.DataBags.Items(versionsBag).Get(ctx, tool)
	switch {
	case errors.Is(err, cinc.ErrNotFound):
		return "", nil
	case err != nil:
		return "", fmt.Errorf("read %s/%s from %s: %w", versionsBag, tool, c.server, err)
	}

	version, _ := item[versionField].(string)
	if version == "" {
		return "", fmt.Errorf("%s/%s on %s has no %q", versionsBag, tool, c.server, versionField)
	}
	return version, nil
}

// ClearPin removes tool's pin entirely.
//
// This is how a run puts back a pin that was not there before it ran. Writing
// an empty version would not do it: nothing reads an item with no version and a
// converge against one fails, so an absent pin and a blank one are different
// states and only the absent one is where a first upgrade started.
//
// An item that is already gone is the wanted state, not a failure.
func (c *Cinc) ClearPin(ctx context.Context, tool string) error {
	if tool == "" {
		return errors.New("tool is required")
	}

	_, err := c.client.DataBags.Items(versionsBag).Delete(ctx, tool)
	if err != nil && !errors.Is(err, cinc.ErrNotFound) {
		return fmt.Errorf("delete %s/%s from %s: %w", versionsBag, tool, c.server, err)
	}
	return nil
}

// SetPin records the version tool is to be installed at, creating the bag and
// the item when they are not there yet.
//
// The update is tried first because the steady state is that the item exists:
// a tool is pinned once and moved many times. Creating is the path a newly
// onboarded tool takes, and it is taken on the server's answer rather than on a
// prior existence check, which would be a second round trip that is out of date
// the moment it returns.
func (c *Cinc) SetPin(ctx context.Context, tool, version string) error {
	if tool == "" {
		return errors.New("tool is required")
	}
	if version == "" {
		return errors.New("version is required")
	}

	items := c.client.DataBags.Items(versionsBag)
	item := cinc.DataBagItem{itemIDField: tool, versionField: version}

	_, _, err := items.Update(ctx, item)
	if err == nil {
		return nil
	}
	if !errors.Is(err, cinc.ErrNotFound) {
		return fmt.Errorf("write %s/%s to %s: %w", versionsBag, tool, c.server, err)
	}

	// A conflict here is another writer having created the bag first, which is
	// the state this call wanted.
	if _, err := c.client.DataBags.Create(ctx, versionsBag); err != nil && !errors.Is(err, cinc.ErrConflict) {
		return fmt.Errorf("create data bag %s on %s: %w", versionsBag, c.server, err)
	}
	if _, _, err := items.Create(ctx, item); err != nil {
		return fmt.Errorf("create %s/%s on %s: %w", versionsBag, tool, c.server, err)
	}
	return nil
}
