// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

func (a *Application) Recover(s *State) error {
	path := filepath.Join(a.Disk.Root, "pending.json")
	data, ok, e := readConfig(path)
	if e != nil {
		return e
	}
	if !ok {
		return errors.New("no unfinished settings transaction")
	}
	var pending Pending
	if json.Unmarshal(data, &pending) != nil || pending.Record.Client == "" || len(pending.Record.Files) == 0 || pending.Desired.Profiles == nil {
		return errors.New("invalid recovery record; preserve it for manual recovery")
	}
	release, e := a.Disk.nativeLocks(pending.Locks)
	if e != nil {
		return e
	}
	defer release()
	record := pending.Record
	actual, _ := json.Marshal(s)
	intended, _ := json.Marshal(pending.Desired)
	if bytes.Equal(actual, intended) {
		for _, b := range record.Files {
			current, exists, e := readConfig(b.Path)
			if e != nil || !exists || !bytes.Equal(current, b.After) {
				return ErrConflict
			}
		}
		return os.Remove(path)
	}

	for _, b := range record.Files {
		current, exists, e := readConfig(b.Path)
		if e != nil {
			return e
		}
		if exists == b.Existed && bytes.Equal(current, b.Before) {
			continue
		}
		if !exists || !bytes.Equal(current, b.After) {
			return ErrConflict
		}
	}
	if !a.Prompt.Confirm("Roll back the unfinished settings transaction using its protected backups?") {
		return errors.New("recovery declined")
	}
	for _, b := range record.Files {
		current, exists, e := readConfig(b.Path)
		if e != nil {
			return e
		}
		if exists == b.Existed && bytes.Equal(current, b.Before) {
			continue
		}
		if !bytes.Equal(current, b.After) {
			return ErrConflict
		}
		if b.Existed {
			e = a.Disk.write(b.Path, b.Before)
		} else {
			e = os.Remove(b.Path)
		}
		if e != nil {
			return e
		}
	}
	return os.Remove(path)
}
