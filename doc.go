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

// Package dvr captures and replays HTTP requests and responses for Go tests.
//
// A [Recorder] implements [http.RoundTripper] and [io.Closer] and operates in
// one of three [Mode] values:
//
//   - [ModePassthrough]: forwards requests directly to the underlying transport.
//   - [ModeRecord]: forwards requests to the underlying transport and writes each
//     request/response exchange into a compressed archive file.
//   - [ModeReplay]: loads a recorded archive file and responds to matching
//     requests without making network calls.
//
// Construct a recorder explicitly with [New] (calling [Recorder.Close] when
// finished) or [NewForTest] (which automatically closes the recorder via
// [testing.TB.Cleanup]). Behavior can be customized with functional [Option]
// helpers such as [WithMode], [WithFile], [WithTransport], [WithMatcher], and
// [WithObfuscator]. Command-line flag integration is opt-in via
// [RegisterFlags].
//
// When replaying, [DefaultMatcher] matches requests based on HTTP method, URL,
// request body, headers, and trailers, marking each recorded entry as consumed
// once matched. If no recorded entry matches, [Recorder.RoundTrip] returns a
// [*NoMatchError] (which satisfies `errors.Is(err, ErrNoMatch)`). Custom
// matching logic can be supplied with [WithMatcher].
//
// Sensitive request data (such as credentials) can be scrubbed before saving to
// disk using [WithObfuscator] and helpers such as [BasicAuthObfuscator].
//
// Common standard library errors and sentinel errors (such as [io.EOF],
// [io.ErrUnexpectedEOF], [context.Canceled], and [context.DeadlineExceeded])
// are preserved across record and replay. Additional concrete error types can
// be registered with [RegisterErrorType]; unregistered errors fall back to an
// error preserving the original message.
//
// This library is intended for use during testing and is not recommended for
// production HTTP traffic.
package dvr
