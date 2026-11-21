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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	"github.com/liquidgecka/testlib"
)

type customError string

func (p customError) Error() string {
	return string(p)
}

type registeredStructError struct {
	Code    int
	Message string
}

func (e *registeredStructError) Error() string {
	return e.Message
}

func init() {
	RegisterErrorType(new(registeredStructError))
}

func TestGobError_GobEncode(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	roundTrip := func(err error) error {
		g := &gobError{Error: err}
		var buf bytes.Buffer
		T.ExpectSuccess(gob.NewEncoder(&buf).Encode(g))
		var out gobError
		T.ExpectSuccess(gob.NewDecoder(&buf).Decode(&out))
		return out.Error
	}

	// Unregistered error falls back to gobSafeError with matching text.
	got := roundTrip(customError("Unexpected"))
	safeErr, ok := got.(gobSafeError)
	T.Equal(ok, true)
	T.Equal(safeErr.Error(), "Unexpected")

	// Built-in registered error type (*net.DNSError) preserves type and fields.
	dnsErr := &net.DNSError{Err: "no such host", Name: "example.invalid"}
	gotDNS := roundTrip(dnsErr)
	T.Equal(gotDNS, dnsErr)
	if _, ok := gotDNS.(*net.DNSError); !ok {
		T.Fatalf("gotDNS is not *net.DNSError, got %T", gotDNS)
	}

	// User-registered error type preserves type and fields.
	regErr := &registeredStructError{Code: 42, Message: "boom"}
	gotReg := roundTrip(regErr)
	T.Equal(gotReg, regErr)

	// errors.New preserves *errors.errorString type.
	errString := errors.New("Expected")
	gotErrString := roundTrip(errString)
	T.Equal(gotErrString, errString)
	T.Equal(reflect.TypeOf(gotErrString), reflect.TypeOf(errString))

	// Sentinel errors preserve identity.
	for _, sentinel := range []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		context.Canceled,
		context.DeadlineExceeded,
	} {
		gotSentinel := roundTrip(sentinel)
		T.Equal(errors.Is(gotSentinel, sentinel), true)
		T.Equal(gotSentinel == sentinel, true)
	}

	// Registered wrapper error wrapping an unregistered inner error falls back cleanly.
	wrapped := &net.OpError{Op: "dial", Net: "tcp", Err: customError("inner")}
	gotWrapped := roundTrip(wrapped)
	T.NotEqual(gotWrapped, nil)
	T.Equal(gotWrapped.Error(), wrapped.Error())

	// Nil and typed-nil errors decode as nil.
	T.Equal(roundTrip(nil), nil)
	var typedNil *net.DNSError
	T.Equal(roundTrip(typedNil), nil)

	// RegisterErrorType ignores nil and handles typed nil safely.
	RegisterErrorType(nil)
	RegisterErrorType(typedNil)
}

func TestGobError_GobDecode(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	var g gobError
	T.ExpectError(g.GobDecode([]byte{0, 1, 2, 3}))

	g.Error = errors.New("stale")
	T.ExpectSuccess(g.GobDecode(nil))
	T.Equal(g.Error, nil)
}

func TestGobQuery_ToRequestResponse(t *testing.T) {
	T := testlib.NewT(t)
	defer T.Finish()

	gq := &gobQuery{
		Request: &gobRequest{URL: "://"},
	}
	_, err := gq.toRequestResponse()
	T.ExpectError(err)

	badCertQuery := &gobQuery{
		Response: &gobResponse{
			TLS: &gobTLSState{
				PeerCertificates: [][]byte{{1, 2, 3}},
			},
		},
	}
	_, err = badCertQuery.toRequestResponse()
	T.ExpectError(err)

	T.Equal(newGobRequest(nil), nil)
	T.Equal(newGobResponse(nil), nil)
	T.Equal(newGobTLSState(nil), nil)

	var nilTLS *gobTLSState
	cs, err := nilTLS.toTLSConnectionState()
	T.ExpectSuccess(err)
	T.Equal(cs, nil)
}
