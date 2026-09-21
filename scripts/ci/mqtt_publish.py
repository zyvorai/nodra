#!/usr/bin/env python3
# Copyright 2026 Zyvor AI Labs · https://zyvor.dev
# SPDX-License-Identifier: Apache-2.0
"""Publish one QoS 0 MQTT 3.1.1 message. Exits 0 when the broker accepts the TCP write."""
import socket
import sys


def enc_str(s: bytes) -> bytes:
    return len(s).to_bytes(2, "big") + s


def packet(ptype: int, body: bytes) -> bytes:
    if len(body) > 127:
        raise SystemExit("packet too large")
    return bytes([ptype, len(body)]) + body


def main() -> int:
    if len(sys.argv) != 5:
        print("usage: mqtt_publish.py host port topic payload", file=sys.stderr)
        return 2
    host, port_s, topic, payload = sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4]
    body = enc_str(b"MQTT") + bytes([4, 0x02, 0, 10]) + enc_str(b"nodra-soak")
    pub = enc_str(topic.encode()) + payload.encode()
    try:
        with socket.create_connection((host, int(port_s)), 5) as sock:
            sock.sendall(packet(0x10, body))
            ack = sock.recv(4)
            if len(ack) < 4 or ack[0] != 0x20 or ack[3] != 0:
                return 1
            sock.sendall(packet(0x30, pub))
            sock.sendall(packet(0xE0, b""))
    except OSError:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
