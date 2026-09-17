// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package opcua

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// Well-known OPC-UA namespace-0 numeric NodeIds for the Subscribe service
// set (Part 6 NodeIds), following the same +3 Request->Response numbering
// as every other service in this package. Recalled from spec knowledge, not
// verified against a real server — see Client.Subscribe's doc comment.
const (
	idCreateSubscriptionRequest    = 787
	idCreateSubscriptionResponse   = 790
	idCreateMonitoredItemsRequest  = 751
	idCreateMonitoredItemsResponse = 754
	idPublishRequest               = 826
	idPublishResponse              = 829
	idDeleteSubscriptionsRequest   = 845
	idDeleteSubscriptionsResponse  = 848

	// idDataChangeNotification is the ExtensionObject TypeId a PublishResponse
	// uses to wrap a DataChangeNotification. Other notification types (e.g.
	// StatusChangeNotification, EventNotificationList) are recognized by a
	// mismatched TypeId and skipped rather than failing the whole response —
	// this connector only monitors the Value attribute, so it never asks a
	// server to send anything else, but a defensive skip costs nothing.
	idDataChangeNotification = 811
)

// createSubscription issues CreateSubscriptionRequest and returns the
// server-assigned SubscriptionId and revised publishing interval.
func (s *session) createSubscription(publishingInterval time.Duration) (subID uint32, revised time.Duration, err error) {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idCreateSubscriptionRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	writeFloat64(body, float64(publishingInterval.Milliseconds())) // RequestedPublishingInterval
	writeUint32(body, 60)                                          // RequestedLifetimeCount
	writeUint32(body, 20)                                          // RequestedMaxKeepAliveCount
	writeUint32(body, 0)                                           // MaxNotificationsPerPublish (0 = no limit)
	body.WriteByte(0x01)                                           // PublishingEnabled = true
	body.WriteByte(0)                                              // Priority

	respBody, err := s.serviceCall(idCreateSubscriptionResponse, body.Bytes())
	if err != nil {
		return 0, 0, err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return 0, 0, fmt.Errorf("status 0x%08x", status)
	}
	subID = r.u32()
	revisedMS := r.f64()
	r.u32() // RevisedLifetimeCount
	r.u32() // RevisedMaxKeepAliveCount
	if r.err != nil {
		return 0, 0, r.err
	}
	return subID, time.Duration(revisedMS) * time.Millisecond, nil
}

// createMonitoredItems requests Value-attribute monitoring for nodeIDs under
// subID, assigning each a 1-based ClientHandle equal to its index+1 in
// nodeIDs — publish() uses that same numbering to map a notification's
// ClientHandle back to the NodeID that changed.
func (s *session) createMonitoredItems(subID uint32, nodeIDs []NodeID, samplingInterval time.Duration) error {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idCreateMonitoredItemsRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	writeUint32(body, subID)
	writeInt32(body, 0) // TimestampsToReturn = Source
	writeInt32(body, int32(len(nodeIDs)))
	for i, id := range nodeIDs {
		body.Write(id.encode())                                      // ItemToMonitor.NodeId
		writeUint32(body, attributeIDValue)                          // ItemToMonitor.AttributeId
		writeString(body, "", true)                                  // ItemToMonitor.IndexRange
		writeUint16(body, 0)                                         // ItemToMonitor.DataEncoding.NamespaceIndex
		writeString(body, "", true)                                  // ItemToMonitor.DataEncoding.Name
		writeInt32(body, 1)                                          // MonitoringMode = Reporting
		writeUint32(body, uint32(i+1))                               // RequestedParameters.ClientHandle
		writeFloat64(body, float64(samplingInterval.Milliseconds())) // RequestedParameters.SamplingInterval
		body.Write([]byte{0x00, 0x00, 0x00})                         // RequestedParameters.Filter: null ExtensionObject
		writeUint32(body, 1)                                         // RequestedParameters.QueueSize
		body.WriteByte(0x01)                                         // RequestedParameters.DiscardOldest = true
	}

	respBody, err := s.serviceCall(idCreateMonitoredItemsResponse, body.Bytes())
	if err != nil {
		return err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return fmt.Errorf("status 0x%08x", status)
	}
	n := r.i32()
	if n < 0 {
		n = 0
	}
	for i := int32(0); i < n; i++ {
		itemStatus := r.u32()
		r.u32()                    // MonitoredItemId
		r.f64()                    // RevisedSamplingInterval
		r.u32()                    // RevisedQueueSize
		readExtensionObjectNull(r) // Filter result (null, since we requested one)
		if itemStatus != 0 {
			return fmt.Errorf("monitored item %d: status 0x%08x", i, itemStatus)
		}
	}
	if r.err != nil {
		return r.err
	}
	return nil
}

// dataChangeItem is one MonitoredItemNotification decoded from a
// DataChangeNotification: which monitored item changed (by ClientHandle,
// 1-based index into the NodeIDs passed to createMonitoredItems) and its new
// value.
type dataChangeItem struct {
	ClientHandle uint32
	Value        DataValue
}

// publish sends one PublishRequest — acknowledging the previous
// notification's sequence number when hasAck is true — and blocks (up to
// timeout) for the server's response, decoding any DataChangeNotifications
// it carries. timeout must be long enough to cover the subscription's
// publishing interval, not the short per-call default: a Publish response
// legitimately doesn't arrive until there's something to report or a
// keep-alive fires.
func (s *session) publish(subID uint32, ackSeq uint32, hasAck bool, timeout time.Duration) (notifications []dataChangeItem, seqNum uint32, err error) {
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idPublishRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	if hasAck {
		writeInt32(body, 1)
		writeUint32(body, subID)
		writeUint32(body, ackSeq)
	} else {
		writeInt32(body, 0)
	}

	respBody, err := s.serviceCallDeadline(idPublishResponse, body.Bytes(), timeout)
	if err != nil {
		return nil, 0, err
	}
	r := &reader{b: respBody}
	status := readResponseHeader(r)
	if status != 0 {
		return nil, 0, fmt.Errorf("status 0x%08x", status)
	}
	gotSubID := r.u32()
	if gotSubID != subID {
		return nil, 0, fmt.Errorf("publish response for subscription %d, want %d", gotSubID, subID)
	}
	navail := r.i32()
	for i := int32(0); i < navail && r.err == nil; i++ {
		r.u32() // AvailableSequenceNumbers
	}
	r.u8() // MoreNotifications — not acted on: the next Publish loop iteration picks up any remainder.
	seqNum = r.u32()
	r.i64() // PublishTime
	ndata := r.i32()
	if ndata < 0 {
		ndata = 0
	}
	for i := int32(0); i < ndata; i++ {
		items, derr := decodeNotificationData(r)
		if derr != nil {
			return nil, 0, derr
		}
		notifications = append(notifications, items...)
	}
	nres := r.i32()
	for i := int32(0); i < nres && r.err == nil; i++ {
		r.u32() // Results[] — StatusCode per acknowledgement we sent
	}
	ndiag := r.i32()
	for i := int32(0); i < ndiag && r.err == nil; i++ {
		readDiagnosticInfo(r)
	}
	if r.err != nil {
		return nil, 0, r.err
	}
	return notifications, seqNum, nil
}

// decodeNotificationData decodes one NotificationData ExtensionObject. A
// null body or a TypeId this connector doesn't recognize (anything but
// DataChangeNotification) yields (nil, nil) rather than an error, since a
// server sending e.g. a keep-alive-adjacent StatusChangeNotification is not
// a protocol violation this connector needs to fail on.
func decodeNotificationData(r *reader) ([]dataChangeItem, error) {
	typeID := readTypeID(r)
	if r.err != nil {
		return nil, r.err
	}
	enc := r.u8()
	if enc == 0 {
		return nil, nil
	}
	if enc != 1 {
		return nil, fmt.Errorf("unsupported notification encoding %d", enc)
	}
	body := r.byteStr()
	if r.err != nil {
		return nil, r.err
	}
	if typeID != idDataChangeNotification {
		return nil, nil
	}
	br := &reader{b: body}
	n := br.i32()
	if n < 0 {
		n = 0
	}
	out := make([]dataChangeItem, 0, n)
	for i := int32(0); i < n; i++ {
		clientHandle := br.u32()
		dv, err := decodeDataValue(br)
		if err != nil {
			return nil, fmt.Errorf("decode MonitoredItemNotification[%d]: %w", i, err)
		}
		out = append(out, dataChangeItem{ClientHandle: clientHandle, Value: dv})
	}
	nd := br.i32()
	for i := int32(0); i < nd && br.err == nil; i++ {
		readDiagnosticInfo(br)
	}
	if br.err != nil {
		return nil, br.err
	}
	return out, nil
}

// deleteSubscriptions is a best-effort cleanup call, ignoring the per-item
// StatusCode results — the connection is about to be closed either way.
func (s *session) deleteSubscriptions(subIDs []uint32) error {
	if len(subIDs) == 0 {
		return nil
	}
	handle := s.nextHandle()
	body := &bytes.Buffer{}
	body.Write(typeIDBytes(idDeleteSubscriptionsRequest))
	body.Write(s.requestHeader(s.authToken, handle))
	writeInt32(body, int32(len(subIDs)))
	for _, id := range subIDs {
		writeUint32(body, id)
	}
	_, err := s.serviceCall(idDeleteSubscriptionsResponse, body.Bytes())
	return err
}

// Subscribe opens one persistent OPC-UA session and streams Value-attribute
// changes for nodeIDs to handler until ctx is done or an unrecoverable error
// occurs — handler is called with the NodeID from nodeIDs at the changed
// item's index and its new DataValue. Unlike Read/Write/Browse, this holds
// one connection open for as long as the subscription runs: Subscribe is a
// long-lived, server-pushed model, not a per-call request/response, so a
// caller that wants continuous updates should call this once and let it
// block (typically from its own goroutine), reconnecting on error itself if
// desired — see connectors/opcua/poller.go's "subscribe" mode for exactly
// that reconnect-with-backoff wrapper.
//
// SecurityPolicy defaults to None with an anonymous session. Basic256Sha256
// may be selected via Client.Security; without certs or channel crypto the
// dial fails with an actionable error. The numeric TypeIds this depends on
// (CreateSubscription, CreateMonitoredItems, Publish, DataChangeNotification)
// are recalled from the OPC-UA Part 6 spec, not verified against a real
// server: smoke-test against a real server (e.g. open62541) before
// production use — this is the least-verified part of the package.
func (c *Client) Subscribe(ctx context.Context, nodeIDs []NodeID, publishingInterval time.Duration, handler func(NodeID, DataValue)) error {
	if len(nodeIDs) == 0 {
		return errors.New("opcua: at least one NodeID is required")
	}
	to := c.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	s, err := dial(ctx, c.Endpoint, to, c.Security)
	if err != nil {
		return err
	}
	defer s.close()

	subID, revised, err := s.createSubscription(publishingInterval)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	defer func() { _ = s.deleteSubscriptions([]uint32{subID}) }()

	if err := s.createMonitoredItems(subID, nodeIDs, revised); err != nil {
		return fmt.Errorf("create monitored items: %w", err)
	}

	// The server may legitimately hold a Publish request open until the next
	// keep-alive; give it comfortably longer than that before treating
	// silence as a dead connection.
	publishTimeout := revised*24 + to
	var (
		hasAck bool
		ackSeq uint32
	)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		notifications, seq, err := s.publish(subID, ackSeq, hasAck, publishTimeout)
		if err != nil {
			return fmt.Errorf("publish: %w", err)
		}
		hasAck, ackSeq = true, seq
		for _, n := range notifications {
			if n.ClientHandle == 0 || int(n.ClientHandle) > len(nodeIDs) {
				continue
			}
			handler(nodeIDs[n.ClientHandle-1], n.Value)
		}
	}
}
