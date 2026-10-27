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
	if pending.Kind == TransactionRestore {
		return a.finishRestore(s, pending, path)
	}
	if pending.Kind != "" && pending.Kind != TransactionApply {
		return errors.New("unknown transaction kind; preserve journal")
	}
	record := pending.Record
	actual, _ := json.Marshal(s)
	intended, _ := json.Marshal(pending.Desired)
	if bytes.Equal(actual, intended) {
		for _, b := range record.Files {
			current, exists, e := readConfig(b.Path)
			if e != nil || exists == pending.Absent[b.Path] || !bytes.Equal(current, b.After) {
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
		if exists == pending.Absent[b.Path] || !bytes.Equal(current, b.After) {
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
		if exists == pending.Absent[b.Path] || !bytes.Equal(current, b.After) {
			return ErrConflict
		}
		if b.Existed {
			e = a.Disk.write(b.Path, b.Before, compareFile(b.Path, b.After, !pending.Absent[b.Path]))
		} else if exists {
			e = os.Remove(b.Path)
		}
		if e != nil {
			return e
		}
	}
	return os.Remove(path)
}

// Restore recovery moves forward. Rolling backward could resurrect a revoked
// remote key in client settings after disconnect has already completed cleanup.
func (a *Application) finishRestore(s *State, p Pending, journal string) error {
	seen := map[string]bool{}
	for _, b := range p.Record.Files {
		if !filepath.IsAbs(b.Path) || seen[b.Path] {
			return errors.New("invalid restore journal")
		}
		seen[b.Path] = true
		current, exists, e := readConfig(b.Path)
		if e != nil {
			return e
		}
		before := exists == b.Existed && bytes.Equal(current, b.Before)
		after := (!p.Absent[b.Path] && exists && bytes.Equal(current, b.After)) || (p.Absent[b.Path] && !exists)
		if !before && !after {
			return ErrConflict
		}
	}
	for path := range p.Absent {
		if !seen[path] {
			return errors.New("invalid restore removal target")
		}
	}
	if !a.Prompt.Confirm("Complete the interrupted restoration? Unchanged pending files will be restored and completed files kept.") {
		return errors.New("recovery declined")
	}
	for _, b := range p.Record.Files {
		current, exists, e := readConfig(b.Path)
		if e != nil {
			return e
		}
		if p.Absent[b.Path] && !exists {
			continue
		}
		if !p.Absent[b.Path] && exists && bytes.Equal(current, b.After) {
			continue
		}
		if exists != b.Existed || !bytes.Equal(current, b.Before) {
			return ErrConflict
		}
		if p.Absent[b.Path] {
			if e = compareFile(b.Path, b.Before, b.Existed)(); e == nil {
				if exists {
					e = os.Remove(b.Path)
				}
			}
		} else {
			e = a.Disk.write(b.Path, b.After, compareFile(b.Path, b.Before, b.Existed))
		}
		if e != nil {
			return e
		}
	}
	// Disconnect may already have removed credentials/profile metadata while
	// this journal was pending. Merge only restoration ownership cleanup.
	desired := copyState(*s)
	delete(desired.Records, p.Record.Client)
	delete(desired.Active, p.Record.Client)
	if e := a.Disk.Save(desired); e != nil {
		return e
	}
	*s = desired
	return os.Remove(journal)
}
