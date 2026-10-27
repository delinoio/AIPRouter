// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"testing"
)

func TestYAMLFlowInsertionPreservesExistingSource(t *testing.T) {
	for _, test := range []struct {
		source string
		path   []string
		value  any
	}{
		{"providers: {foreign: {api: 'unchanged'}} # keep\n", []string{"providers", "aiprouter"}, object{"api": "endpoint"}},
		{"providers: [{id: foreign, name: 'Keep'}] # keep\n", []string{"providers", "[id=aiprouter]"}, object{"id": "aiprouter", "name": "New"}},
		{"providers: [] # keep\n", []string{"providers", "[id=aiprouter]"}, object{"id": "aiprouter"}},
	} {
		after, e := Patch([]byte(test.source), YAML, []Fragment{{test.path, test.value}}, false)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = parseDocument(after, YAML); e != nil {
			t.Fatal(e)
		}
		restored, e := restoreBytes(Backup{Before: []byte(test.source), After: after, Existed: true}, after)
		if e != nil || !bytes.Equal(restored, []byte(test.source)) {
			t.Fatal("source restoration failed")
		}
		if !bytes.Contains(after, []byte("# keep")) {
			t.Fatal("comment removed")
		}
	}
}
