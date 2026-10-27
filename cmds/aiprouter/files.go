// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type Protector interface{ Protect(string, bool) error }
type NativeProtector struct{}
type Disk struct {
	Root       string
	Protection Protector
}
type State struct {
	Profiles map[string]Profile `json:"profiles"`
	Active   map[string]string  `json:"active"`
	Records  map[string]Record  `json:"records"`
}
type Record struct {
	Profile, Client string
	Files           []Backup
}
type Backup struct {
	Path          string
	Before, After []byte
	Existed       bool
}
type Change struct {
	Path          string
	Before, After []byte
	Existed       bool
	Fields        []string
	RemoteKey     bool
}

func emptyState() State {
	return State{Profiles: map[string]Profile{}, Active: map[string]string{}, Records: map[string]Record{}}
}
func (d Disk) init() error {
	if !filepath.IsAbs(d.Root) {
		return errors.New("state directory must be absolute")
	}
	if err := safeParents(d.Root); err != nil {
		return err
	}
	if err := os.MkdirAll(d.Root, 0700); err != nil {
		return err
	}
	return d.Protection.Protect(d.Root, true)
}
func safeParents(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e == nil && s.Mode()&os.ModeSymlink != 0 {
			return errors.New("symbolic links are not accepted in managed paths")
		}
		if e != nil && !os.IsNotExist(e) {
			return errors.New("cannot inspect managed path")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func readConfig(path string) ([]byte, bool, error) {
	if err := safeParents(path); err != nil {
		return nil, false, err
	}
	s, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || !s.Mode().IsRegular() || s.Size() > MaxConfigBytes {
		return nil, false, errors.New("configuration must be a readable regular file of at most 4 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, errors.New("cannot open configuration")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(s, opened) {
		return nil, false, ErrConflict
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil || len(b) > MaxConfigBytes {
		return nil, false, errors.New("cannot read bounded configuration")
	}
	current, e := os.Lstat(path)
	if e != nil || !os.SameFile(s, current) {
		return nil, false, ErrConflict
	}
	return b, true, nil
}
func (d Disk) write(path string, b []byte) error {
	if len(b) > MaxConfigBytes {
		return errors.New("configuration exceeds 4 MiB")
	}
	if err := safeParents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".aiprouter-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = d.Protection.Protect(tmp, false); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = safeParents(path); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func (d Disk) Load() (State, error) {
	if err := d.init(); err != nil {
		return State{}, err
	}
	b, exists, err := readConfig(filepath.Join(d.Root, "state.json"))
	if err != nil {
		return State{}, err
	}
	s := emptyState()
	if !exists {
		return s, nil
	}
	if json.Unmarshal(b, &s) != nil || s.Profiles == nil || s.Active == nil || s.Records == nil {
		return State{}, errors.New("state is invalid; preserve it for recovery")
	}
	return s, nil
}
func (d Disk) Save(s State) error {
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return d.write(filepath.Join(d.Root, "state.json"), b)
}
func (d Disk) Lock() (func(), error) {
	if err := d.init(); err != nil {
		return nil, err
	}
	p := filepath.Join(d.Root, "write.lock")
	if err := os.Mkdir(p, 0700); err != nil {
		return nil, errors.New("another CLI operation is active; if it crashed, inspect recovery state before removing write.lock")
	}
	return func() { _ = os.Remove(p) }, nil
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Preview lists field names and fingerprints, never source values. Existing
// settings and backups can contain arbitrary secrets we cannot enumerate.
func Preview(out io.Writer, changes []Change) {
	for _, c := range changes {
		fmt.Fprintf(out, "%s\n  fields: %v\n  before: %s\n  after: %s\n  values: [redacted]\n", c.Path, c.Fields, digest(c.Before), digest(c.After))
	}
}
func (d Disk) Apply(s *State, client, profile string, changes []Change, prompt Prompter) error {
	if len(changes) == 0 {
		return errors.New("no configuration changes")
	}
	for _, c := range changes {
		if c.RemoteKey && !prompt.Confirm("Store the remote API key in this client's protected settings (separate from the OS store)?") {
			return errors.New("remote-key storage declined")
		}
	}
	if !prompt.Confirm("Apply the displayed settings changes and save protected recovery backups?") {
		return errors.New("settings changes declined")
	}
	for _, c := range changes {
		b, exists, e := readConfig(c.Path)
		if e != nil {
			return e
		}
		if exists != c.Existed || !bytes.Equal(b, c.Before) {
			return ErrConflict
		}
	}
	// Durable pending evidence precedes the first client write. Recovery never
	// guesses whether a partial multi-file operation committed.
	record := Record{Profile: profile, Client: client}
	for _, c := range changes {
		record.Files = append(record.Files, Backup{c.Path, c.Before, c.After, c.Existed})
	}
	pending := filepath.Join(d.Root, "pending.json")
	b, _ := json.Marshal(record)
	if err := d.write(pending, b); err != nil {
		return err
	}
	for _, c := range changes {
		current, exists, e := readConfig(c.Path)
		if e != nil {
			return e
		}
		if exists != c.Existed || !bytes.Equal(current, c.Before) {
			return ErrConflict
		}
		if err := d.write(c.Path, c.After); err != nil {
			return errors.New("settings write failed; pending recovery retained")
		}
	}
	// Profile switches retain the original backup, while tracking the newly
	// applied bytes. This keeps disconnect from resurrecting an obsolete key.
	if prior, ok := s.Records[client]; ok {
		for i := range record.Files {
			for _, old := range prior.Files {
				if old.Path == record.Files[i].Path {
					record.Files[i].Before = old.Before
					record.Files[i].Existed = old.Existed
				}
			}
		}
	}
	s.Records[client] = record
	s.Active[client] = profile
	if err := d.Save(*s); err != nil {
		return err
	}
	return os.Remove(pending)
}
func restoreBytes(b Backup, current []byte) ([]byte, error) {
	if bytes.Equal(current, b.After) {
		return b.Before, nil
	}
	// A minimal source span permits unrelated edits before/after the owned
	// contribution. A changed or ambiguous owned span refuses restoration.
	prefix := 0
	for prefix < len(b.Before) && prefix < len(b.After) && b.Before[prefix] == b.After[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(b.Before)-prefix && suffix < len(b.After)-prefix && b.Before[len(b.Before)-1-suffix] == b.After[len(b.After)-1-suffix] {
		suffix++
	}
	old := b.Before[prefix : len(b.Before)-suffix]
	owned := b.After[prefix : len(b.After)-suffix]
	if len(owned) == 0 || bytes.Count(current, owned) != 1 {
		return nil, ErrConflict
	}
	return bytes.Replace(current, owned, old, 1), nil
}
func (d Disk) Restore(s *State, client string) error {
	r, ok := s.Records[client]
	if !ok {
		return nil
	}
	type restored struct {
		b               Backup
		current, result []byte
	}
	var files []restored
	for _, b := range r.Files {
		current, exists, e := readConfig(b.Path)
		if e != nil {
			return e
		}
		if !exists {
			return ErrConflict
		}
		result, e := restoreBytes(b, current)
		if e != nil {
			return e
		}
		if !b.Existed && !bytes.Equal(current, b.After) {
			return ErrConflict
		}
		files = append(files, restored{b, current, result})
	}
	for _, f := range files {
		current, _, e := readConfig(f.b.Path)
		if e != nil || !bytes.Equal(current, f.current) {
			return ErrConflict
		}
		if f.b.Existed {
			if e = d.write(f.b.Path, f.result); e != nil {
				return e
			}
		} else if e = os.Remove(f.b.Path); e != nil {
			return e
		}
	}
	delete(s.Records, client)
	delete(s.Active, client)
	return d.Save(*s)
}
