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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

func (r *Recorder) openForRecord() error {
	if dir := filepath.Dir(r.filePath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("dvr: create archive directory: %w", err)
		}
	}

	f, err := os.OpenFile(r.filePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("dvr: open archive for writing: %w", err)
	}

	if err := binary.Write(f, binary.BigEndian, archiveVersion); err != nil {
		_ = f.Close()
		return fmt.Errorf("dvr: write archive version: %w", err)
	}

	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("dvr: create gzip writer: %w", err)
	}

	r.file = f
	r.gzipWriter = gz
	r.tarWriter = tar.NewWriter(gz)
	return nil
}

func (r *Recorder) record(req *http.Request) (*http.Response, error) {
	q := &gobQuery{
		Request: newGobRequest(req),
	}

	outReq := req.Clone(req.Context())
	if req.Body != nil {
		var reqBuf bytes.Buffer
		_, reqBodyErr := io.Copy(&reqBuf, req.Body)
		_ = req.Body.Close()

		q.Request.Body = reqBuf.Bytes()
		q.Request.Error.Error = reqBodyErr

		outReq.Body = &bodyReader{
			data: q.Request.Body,
			err:  reqBodyErr,
		}
	}

	resp, realErr := r.transport.RoundTrip(outReq)
	if resp != nil && (resp.Request == nil || resp.Request == outReq) {
		resp.Request = req
	}
	q.Error.Error = realErr
	q.Response = newGobResponse(resp)

	if resp != nil && resp.Body != nil {
		var respBuf bytes.Buffer
		_, respBodyErr := io.Copy(&respBuf, resp.Body)
		_ = resp.Body.Close()

		q.Response.Body = respBuf.Bytes()
		q.Response.Error.Error = respBodyErr

		resp.Body = &bodyReader{
			data: q.Response.Body,
			err:  respBodyErr,
		}
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(q); err != nil {
		return resp, fmt.Errorf("dvr: encode recorded query: %w", err)
	}

	if r.obfuscator != nil {
		var qCopy gobQuery
		if err := gob.NewDecoder(&buf).Decode(&qCopy); err != nil {
			return resp, fmt.Errorf("dvr: decode query for obfuscation: %w", err)
		}

		rr, err := qCopy.toRequestResponse()
		if err != nil {
			return resp, err
		}
		r.obfuscator(rr)

		qObf := &gobQuery{
			Request:  newGobRequest(rr.Request),
			Response: newGobResponse(rr.Response),
			Error:    gobError{Error: rr.Error},
		}
		if qObf.Request != nil {
			qObf.Request.Body = rr.RequestBody
			qObf.Request.Error.Error = rr.RequestBodyError
		}
		if qObf.Response != nil {
			qObf.Response.Body = rr.ResponseBody
			qObf.Response.Error.Error = rr.ResponseBodyError
		}

		var obfBuf bytes.Buffer
		if err := gob.NewEncoder(&obfBuf).Encode(qObf); err != nil {
			return resp, fmt.Errorf("dvr: encode obfuscated query: %w", err)
		}
		buf = obfBuf
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return nil, ErrClosed
	}

	header := &tar.Header{
		Name:     strconv.Itoa(r.writerCount),
		Mode:     0o600,
		Size:     int64(buf.Len()),
		Typeflag: tar.TypeReg,
	}
	r.writerCount++

	if err := r.tarWriter.WriteHeader(header); err != nil {
		return resp, fmt.Errorf("dvr: write tar header: %w", err)
	}
	if _, err := r.tarWriter.Write(buf.Bytes()); err != nil {
		return resp, fmt.Errorf("dvr: write tar body: %w", err)
	}
	if err := r.tarWriter.Flush(); err != nil {
		return resp, fmt.Errorf("dvr: flush tar writer: %w", err)
	}

	return resp, realErr
}
