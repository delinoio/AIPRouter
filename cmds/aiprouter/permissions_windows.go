// SPDX-License-Identifier: Apache-2.0
package main

import (
	"errors"
	"os/exec"
	"os/user"
)

func (NativeProtector) Protect(path string, dir bool) error {
	u, e := user.Current()
	if e != nil {
		return errors.New("cannot resolve Windows owner for ACL")
	}
	grant := "*" + u.Uid + ":F"
	if dir {
		grant = "*" + u.Uid + ":(OI)(CI)F"
	}
	// Protect the empty temporary file before writing any secret-bearing bytes.
	if exec.Command("icacls.exe", path, "/inheritance:r", "/grant:r", grant).Run() != nil {
		return errors.New("cannot apply owner-only Windows ACL")
	}
	return nil
}
