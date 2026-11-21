// Copyright 2014 Orchestrate, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dvr

import (
	"flag"
	"path/filepath"
	"testing"

	"github.com/liquidgecka/testlib"
)

func TestFlagsModePrecedence(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	cases := []struct {
		rec, rep, pass, defReplay bool
		want                      Mode
	}{
		{true, false, false, false, ModeRecord},
		{true, false, false, true, ModeRecord},
		{true, false, true, false, ModeRecord},
		{true, false, true, true, ModeRecord},
		{true, true, false, false, ModeRecord},
		{true, true, false, true, ModeRecord},
		{true, true, true, false, ModeRecord},
		{true, true, true, true, ModeRecord},

		{false, true, false, false, ModeReplay},
		{false, true, false, true, ModeReplay},
		{false, true, true, false, ModeReplay},
		{false, true, true, true, ModeReplay},

		{false, false, true, false, ModePassthrough},
		{false, false, true, true, ModePassthrough},

		{false, false, false, true, ModeReplay},
		{false, false, false, false, ModePassthrough},
	}

	for _, tc := range cases {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		flags := RegisterFlags(fs)
		flags.record = tc.rec
		flags.replay = tc.rep
		flags.passthrough = tc.pass
		flags.SetDefaultReplay(tc.defReplay)

		T.Equal(flags.Mode(), tc.want)
		T.Equal(flags.File(), DefaultArchiveFile)
	}
}

func TestFlagsParsingAndOptions(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	archivePath := filepath.Join(T.TempDir(), "flags.dvr")

	fs := flag.NewFlagSet("dvr-flags-test", flag.ContinueOnError)
	flags := RegisterFlags(fs)
	err := fs.Parse([]string{"-dvr.record", "-dvr.file", archivePath})
	T.ExpectSuccess(err)

	T.Equal(flags.Mode(), ModeRecord)
	T.Equal(flags.File(), archivePath)

	rec, err := New(flags.Options()...)
	T.ExpectSuccess(err)
	T.Equal(rec.Mode(), ModeRecord)
	T.ExpectSuccess(rec.Close())
}
