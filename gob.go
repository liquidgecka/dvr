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
	"crypto/tls"
	"crypto/x509"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sync"
)

const archiveVersion uint32 = 1

// gobSafeError is a gob-encodable string error used as a fallback when an
// error value's concrete type is not registered with encoding/gob.
type gobSafeError string

// Error implements the error interface.
func (g gobSafeError) Error() string {
	return string(g)
}

var (
	encodableTypesMu sync.RWMutex
	encodableTypes   = map[string]bool{}
)

var knownSentinels = []struct {
	name string
	err  error
}{
	{name: "io.EOF", err: io.EOF},
	{name: "io.ErrUnexpectedEOF", err: io.ErrUnexpectedEOF},
	{name: "context.Canceled", err: context.Canceled},
	{name: "context.DeadlineExceeded", err: context.DeadlineExceeded},
}

var sentinelByName = map[string]error{
	"io.EOF":                   io.EOF,
	"io.ErrUnexpectedEOF":      io.ErrUnexpectedEOF,
	"context.Canceled":         context.Canceled,
	"context.DeadlineExceeded": context.DeadlineExceeded,
}

func init() {
	RegisterErrorType(gobSafeError(""))
	RegisterErrorType(new(net.AddrError))
	RegisterErrorType(new(net.DNSConfigError))
	RegisterErrorType(new(net.DNSError))
	RegisterErrorType(net.InvalidAddrError(""))
	RegisterErrorType(new(net.OpError))
	RegisterErrorType(new(net.ParseError))
	RegisterErrorType(net.UnknownNetworkError(""))
	RegisterErrorType(new(url.Error))
	RegisterErrorType(url.EscapeError(""))
	RegisterErrorType(url.InvalidHostError(""))

	gob.Register(new(net.TCPAddr))
	gob.Register(new(net.UDPAddr))
	gob.Register(new(net.IPAddr))
}

// RegisterErrorType registers err's concrete type with encoding/gob so that
// error values of that type preserve their dynamic type across record and
// replay. Calls should typically be made during package initialization before
// recording or replaying begins.
func RegisterErrorType(err error) {
	if err == nil {
		return
	}

	gob.Register(err)

	typ := reflect.TypeOf(err)
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	id := fmt.Sprintf("%s.%s", typ.PkgPath(), typ.Name())

	encodableTypesMu.Lock()
	encodableTypes[id] = true
	encodableTypesMu.Unlock()
}

type gobRawError struct {
	Error             error
	ErrorsErrorString bool
	Sentinel          string
}

type gobError struct {
	Error error
}

func (g gobError) GobEncode() ([]byte, error) {
	if g.Error == nil {
		return []byte{}, nil
	}

	for _, s := range knownSentinels {
		if g.Error == s.err {
			var buf bytes.Buffer
			err := gob.NewEncoder(&buf).Encode(&gobRawError{Sentinel: s.name})
			return buf.Bytes(), err
		}
	}

	value := reflect.ValueOf(g.Error)
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return []byte{}, nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		return []byte{}, nil
	}

	typ := value.Type()
	id := fmt.Sprintf("%s.%s", typ.PkgPath(), typ.Name())

	encodableTypesMu.RLock()
	registered := encodableTypes[id]
	encodableTypesMu.RUnlock()

	if registered {
		var buf bytes.Buffer
		raw := gobRawError{Error: g.Error}
		if err := gob.NewEncoder(&buf).Encode(&raw); err == nil {
			return buf.Bytes(), nil
		}
	}

	raw := gobRawError{
		Error:             gobSafeError(g.Error.Error()),
		ErrorsErrorString: id == "errors.errorString",
	}
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(&raw)
	return buf.Bytes(), err
}

func (g *gobError) GobDecode(data []byte) error {
	if len(data) == 0 {
		g.Error = nil
		return nil
	}

	var rawError gobRawError
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&rawError); err != nil {
		return err
	}

	switch {
	case rawError.Sentinel != "":
		if err, ok := sentinelByName[rawError.Sentinel]; ok {
			g.Error = err
			return nil
		}
		g.Error = gobSafeError(rawError.Sentinel)
	case rawError.ErrorsErrorString:
		g.Error = errors.New(rawError.Error.Error())
	default:
		g.Error = rawError.Error
	}
	return nil
}

type gobTLSState struct {
	Version                     uint16
	HandshakeComplete           bool
	DidResume                   bool
	CipherSuite                 uint16
	CurveID                     tls.CurveID
	NegotiatedProtocol          string
	ServerName                  string
	PeerCertificates            [][]byte
	VerifiedChains              [][][]byte
	SignedCertificateTimestamps [][]byte
	OCSPResponse                []byte
	TLSUnique                   []byte
	ECHAccepted                 bool
	HelloRetryRequest           bool
}

func newGobTLSState(cs *tls.ConnectionState) *gobTLSState {
	if cs == nil {
		return nil
	}

	state := &gobTLSState{
		Version:                     cs.Version,
		HandshakeComplete:           cs.HandshakeComplete,
		DidResume:                   cs.DidResume,
		CipherSuite:                 cs.CipherSuite,
		CurveID:                     cs.CurveID,
		NegotiatedProtocol:          cs.NegotiatedProtocol,
		ServerName:                  cs.ServerName,
		SignedCertificateTimestamps: cs.SignedCertificateTimestamps,
		OCSPResponse:                cs.OCSPResponse,
		TLSUnique:                   cs.TLSUnique,
		ECHAccepted:                 cs.ECHAccepted,
		HelloRetryRequest:           cs.HelloRetryRequest,
	}

	if len(cs.PeerCertificates) > 0 {
		state.PeerCertificates = make([][]byte, len(cs.PeerCertificates))
		for i, cert := range cs.PeerCertificates {
			if cert != nil {
				state.PeerCertificates[i] = cert.Raw
			}
		}
	}

	if len(cs.VerifiedChains) > 0 {
		state.VerifiedChains = make([][][]byte, len(cs.VerifiedChains))
		for i, chain := range cs.VerifiedChains {
			state.VerifiedChains[i] = make([][]byte, len(chain))
			for j, cert := range chain {
				if cert != nil {
					state.VerifiedChains[i][j] = cert.Raw
				}
			}
		}
	}

	return state
}

func (s *gobTLSState) toTLSConnectionState() (*tls.ConnectionState, error) {
	if s == nil {
		return nil, nil
	}

	cs := &tls.ConnectionState{
		Version:                     s.Version,
		HandshakeComplete:           s.HandshakeComplete,
		DidResume:                   s.DidResume,
		CipherSuite:                 s.CipherSuite,
		CurveID:                     s.CurveID,
		NegotiatedProtocol:          s.NegotiatedProtocol,
		ServerName:                  s.ServerName,
		SignedCertificateTimestamps: s.SignedCertificateTimestamps,
		OCSPResponse:                s.OCSPResponse,
		TLSUnique:                   s.TLSUnique,
		ECHAccepted:                 s.ECHAccepted,
		HelloRetryRequest:           s.HelloRetryRequest,
	}

	if len(s.PeerCertificates) > 0 {
		cs.PeerCertificates = make([]*x509.Certificate, len(s.PeerCertificates))
		for i, raw := range s.PeerCertificates {
			if len(raw) == 0 {
				continue
			}
			cert, err := x509.ParseCertificate(raw)
			if err != nil {
				return nil, fmt.Errorf("dvr: parse peer certificate: %w", err)
			}
			cs.PeerCertificates[i] = cert
		}
	}

	if len(s.VerifiedChains) > 0 {
		cs.VerifiedChains = make([][]*x509.Certificate, len(s.VerifiedChains))
		for i, chain := range s.VerifiedChains {
			cs.VerifiedChains[i] = make([]*x509.Certificate, len(chain))
			for j, raw := range chain {
				if len(raw) == 0 {
					continue
				}
				cert, err := x509.ParseCertificate(raw)
				if err != nil {
					return nil, fmt.Errorf("dvr: parse verified chain certificate: %w", err)
				}
				cs.VerifiedChains[i][j] = cert
			}
		}
	}

	return cs, nil
}

type gobRequest struct {
	Method           string
	URL              string
	Proto            string
	ProtoMajor       int
	ProtoMinor       int
	Header           http.Header
	ContentLength    int64
	TransferEncoding []string
	Close            bool
	Host             string
	Form             url.Values
	PostForm         url.Values
	Trailer          http.Header
	RemoteAddr       string
	RequestURI       string
	TLS              *gobTLSState

	Body  []byte
	Error gobError
}

func newGobRequest(req *http.Request) *gobRequest {
	if req == nil {
		return nil
	}

	var rawURL string
	if req.URL != nil {
		rawURL = req.URL.String()
	}

	return &gobRequest{
		Method:           req.Method,
		URL:              rawURL,
		Proto:            req.Proto,
		ProtoMajor:       req.ProtoMajor,
		ProtoMinor:       req.ProtoMinor,
		Header:           req.Header,
		ContentLength:    req.ContentLength,
		TransferEncoding: req.TransferEncoding,
		Close:            req.Close,
		Host:             req.Host,
		Form:             req.Form,
		PostForm:         req.PostForm,
		Trailer:          req.Trailer,
		RemoteAddr:       req.RemoteAddr,
		RequestURI:       req.RequestURI,
		TLS:              newGobTLSState(req.TLS),
	}
}

type gobResponse struct {
	Status           string
	StatusCode       int
	Proto            string
	ProtoMajor       int
	ProtoMinor       int
	Header           http.Header
	ContentLength    int64
	TransferEncoding []string
	Close            bool
	Uncompressed     bool
	Trailer          http.Header
	TLS              *gobTLSState

	Body  []byte
	Error gobError
}

func newGobResponse(resp *http.Response) *gobResponse {
	if resp == nil {
		return nil
	}

	return &gobResponse{
		Status:           resp.Status,
		StatusCode:       resp.StatusCode,
		Proto:            resp.Proto,
		ProtoMajor:       resp.ProtoMajor,
		ProtoMinor:       resp.ProtoMinor,
		Header:           resp.Header,
		ContentLength:    resp.ContentLength,
		TransferEncoding: resp.TransferEncoding,
		Close:            resp.Close,
		Uncompressed:     resp.Uncompressed,
		Trailer:          resp.Trailer,
		TLS:              newGobTLSState(resp.TLS),
	}
}

type gobQuery struct {
	Request  *gobRequest
	Response *gobResponse
	Error    gobError
}

func (g *gobQuery) toRequestResponse() (*RequestResponse, error) {
	rr := &RequestResponse{
		Error: g.Error.Error,
	}

	if g.Request != nil {
		var parsedURL *url.URL
		if g.Request.URL != "" {
			var err error
			parsedURL, err = url.Parse(g.Request.URL)
			if err != nil {
				return nil, fmt.Errorf("dvr: parse recorded request URL: %w", err)
			}
		}

		tlsState, err := g.Request.TLS.toTLSConnectionState()
		if err != nil {
			return nil, err
		}

		rr.Request = &http.Request{
			Method:           g.Request.Method,
			URL:              parsedURL,
			Proto:            g.Request.Proto,
			ProtoMajor:       g.Request.ProtoMajor,
			ProtoMinor:       g.Request.ProtoMinor,
			Header:           g.Request.Header,
			ContentLength:    g.Request.ContentLength,
			TransferEncoding: g.Request.TransferEncoding,
			Close:            g.Request.Close,
			Host:             g.Request.Host,
			Form:             g.Request.Form,
			PostForm:         g.Request.PostForm,
			Trailer:          g.Request.Trailer,
			RemoteAddr:       g.Request.RemoteAddr,
			RequestURI:       g.Request.RequestURI,
			TLS:              tlsState,
		}
		rr.RequestBody = g.Request.Body
		rr.RequestBodyError = g.Request.Error.Error
	}

	if g.Response != nil {
		tlsState, err := g.Response.TLS.toTLSConnectionState()
		if err != nil {
			return nil, err
		}

		rr.Response = &http.Response{
			Status:           g.Response.Status,
			StatusCode:       g.Response.StatusCode,
			Proto:            g.Response.Proto,
			ProtoMajor:       g.Response.ProtoMajor,
			ProtoMinor:       g.Response.ProtoMinor,
			Header:           g.Response.Header,
			ContentLength:    g.Response.ContentLength,
			TransferEncoding: g.Response.TransferEncoding,
			Close:            g.Response.Close,
			Uncompressed:     g.Response.Uncompressed,
			Trailer:          g.Response.Trailer,
			TLS:              tlsState,
		}
		rr.ResponseBody = g.Response.Body
		rr.ResponseBodyError = g.Response.Error.Error
	}

	return rr, nil
}
