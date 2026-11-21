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
	"encoding/base64"
	"net/url"
)

// BasicAuthObfuscator returns an [ObfuscateFunc] that replaces any HTTP Basic
// Auth credentials in the Authorization header and URL userinfo with username
// and password before the request is written to an archive.
func BasicAuthObfuscator(username, password string) ObfuscateFunc {
	authValue := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	return func(rr *RequestResponse) {
		if rr == nil || rr.Request == nil {
			return
		}
		if rr.Request.Header != nil && rr.Request.Header.Get("Authorization") != "" {
			rr.Request.Header.Set("Authorization", authValue)
		}
		if rr.Request.URL != nil && rr.Request.URL.User != nil {
			if password == "" {
				rr.Request.URL.User = url.User(username)
			} else {
				rr.Request.URL.User = url.UserPassword(username, password)
			}
		}
	}
}
