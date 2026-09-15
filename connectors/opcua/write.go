// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"context"
	"fmt"
	"time"
)

// Well-known OPC-UA namespace-0 numeric NodeIds for the Write service (Part
// 6 NodeIds — stable across UA 1.0x), matching the +3 Request->Response
// numbering already used for every other service in client.go.
const (
	idWriteRequest  = 671
	idWriteResponse = 674
)

// write issues a WriteRequest for a single node/attribute and returns its
// StatusCode (0 = Good). Mirrors read()'s shape exactly.
func (s *session) write(nodeID NodeID, value any) (uint32, error) {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idWriteRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	writeInt32(body, 1) // NodesToWrite count = 1
	body.Write(nodeID.encode())
	writeUint32(body, attributeIDValue)
	writeString(body, "", true) // IndexRange = null
	if err := encodeDataValueForWrite(body, value); err != nil {
		return 0, err
	}
	respBody, err := s.serviceCall(idWriteResponse, body.Bytes())
	if err != nil {
		return 0, err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return 0, fmt.Errorf("status 0x%08x", status)
	}
	n := r.i32()
	if r.err != nil {
		return 0, r.err
	}
	if n != 1 {
		return 0, fmt.Errorf("write response returned %d status codes, want 1", n)
	}
	statusCode := r.u32()
	if dn := r.i32(); dn > 0 {
		for i := int32(0); i < dn; i++ {
			readDiagnosticInfo(r)
		}
	}
	if r.err != nil {
		return 0, r.err
	}
	return statusCode, nil
}

// Write writes value to a single node's Value attribute over a fresh
// connect/handshake/Write/close cycle, mirroring Client.Read.
func (c *Client) Write(ctx context.Context, nodeID NodeID, value any) (uint32, error) {
	to := c.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	s, err := dial(ctx, c.Endpoint, to)
	if err != nil {
		return 0, err
	}
	defer s.close()
	return s.write(nodeID, value)
}
