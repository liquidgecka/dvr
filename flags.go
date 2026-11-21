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

import "flag"

// Flags holds command-line flag values registered with [RegisterFlags].
type Flags struct {
	record        bool
	replay        bool
	passthrough   bool
	file          string
	defaultReplay bool
}

// RegisterFlags registers -dvr.record, -dvr.replay, -dvr.passthrough, and
// -dvr.file on fs. If fs is nil, [flag.CommandLine] is used.
func RegisterFlags(fs *flag.FlagSet) *Flags {
	if fs == nil {
		fs = flag.CommandLine
	}
	f := &Flags{}
	fs.BoolVar(&f.record, "dvr.record", false,
		"Record HTTP calls into -dvr.file for use later.")
	fs.BoolVar(&f.replay, "dvr.replay", false,
		"Replay HTTP calls from -dvr.file.")
	fs.BoolVar(&f.passthrough, "dvr.passthrough", false,
		"Allow queries to pass through without being recorded or replayed.")
	fs.StringVar(&f.file, "dvr.file", DefaultArchiveFile,
		"The file that stores recorded HTTP calls.")
	return f
}

// SetDefaultReplay configures whether [ModeReplay] is used when none of
// -dvr.record, -dvr.replay, or -dvr.passthrough are explicitly set.
func (f *Flags) SetDefaultReplay(v bool) {
	f.defaultReplay = v
}

// Mode returns the [Mode] selected by the parsed flag values. Flag precedence
// is record > replay > passthrough > defaultReplay > [ModePassthrough].
func (f *Flags) Mode() Mode {
	switch {
	case f.record:
		return ModeRecord
	case f.replay:
		return ModeReplay
	case f.passthrough:
		return ModePassthrough
	case f.defaultReplay:
		return ModeReplay
	default:
		return ModePassthrough
	}
}

// File returns the archive file path configured via -dvr.file.
func (f *Flags) File() string {
	return f.file
}

// Options returns [Option] values derived from the parsed flags suitable for
// passing to [New] or [NewForTest].
func (f *Flags) Options() []Option {
	return []Option{
		WithMode(f.Mode()),
		WithFile(f.File()),
	}
}
