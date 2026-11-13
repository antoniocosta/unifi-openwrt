#!/usr/bin/env python3
# SPDX-License-Identifier: GPL-2.0-or-later
"""Minimal SNMP v2c GET: snmpget.py <host> <community-file> <oid> [<oid>...]
The community is read from a file so it never appears on a command line."""
import socket
import sys


def ber_len(n):
    if n < 0x80:
        return bytes([n])
    b = n.to_bytes((n.bit_length() + 7) // 8, "big")
    return bytes([0x80 | len(b)]) + b


def tlv(tag, body):
    return bytes([tag]) + ber_len(len(body)) + body


def enc_int(v):
    b = v.to_bytes(max(1, (v.bit_length() + 8) // 8), "big", signed=True)
    return tlv(0x02, b)


def enc_oid(oid):
    p = [int(x) for x in oid.strip(".").split(".")]
    out = bytes([p[0] * 40 + p[1]])
    for v in p[2:]:
        chunk = [v & 0x7F]
        v >>= 7
        while v:
            chunk.append(0x80 | (v & 0x7F))
            v >>= 7
        out += bytes(reversed(chunk))
    return tlv(0x06, out)


def read_tlv(b, i):
    tag = b[i]
    n = b[i + 1]
    i += 2
    if n & 0x80:
        k = n & 0x7F
        n = int.from_bytes(b[i:i + k], "big")
        i += k
    return tag, b[i:i + n], i + n


def dec_oid(b):
    out = [b[0] // 40, b[0] % 40]
    v = 0
    for c in b[1:]:
        v = (v << 7) | (c & 0x7F)
        if not c & 0x80:
            out.append(v)
            v = 0
    return ".".join(map(str, out))


def get(host, community, oids, timeout=3):
    vbs = b"".join(tlv(0x30, enc_oid(o) + tlv(0x05, b"")) for o in oids)
    pdu = tlv(0xA0, enc_int(4242) + enc_int(0) + enc_int(0) + tlv(0x30, vbs))
    msg = tlv(0x30, enc_int(1) + tlv(0x04, community.encode()) + pdu)
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.settimeout(timeout)
    s.sendto(msg, (host, 161))
    data, _ = s.recvfrom(65535)
    _, body, _ = read_tlv(data, 0)
    _, _, i = read_tlv(body, 0)          # version
    _, _, i = read_tlv(body, i)          # community
    _, pdu, _ = read_tlv(body, i)
    _, _, j = read_tlv(pdu, 0)           # request id
    _, err, j = read_tlv(pdu, j)
    _, _, j = read_tlv(pdu, j)
    _, vbl, _ = read_tlv(pdu, j)
    res = []
    k = 0
    while k < len(vbl):
        _, vb, k = read_tlv(vbl, k)
        _, oid, m = read_tlv(vb, 0)
        tag, val, _ = read_tlv(vb, m)
        if tag == 0x04:
            v = val.decode("utf-8", "replace")
        elif tag in (0x02, 0x41, 0x42, 0x43, 0x46):
            v = int.from_bytes(val, "big", signed=(tag == 0x02))
        elif tag == 0x06:
            v = dec_oid(val)
        else:
            v = "<tag 0x%02x, %d bytes>" % (tag, len(val))
        res.append((dec_oid(oid), v))
    return int.from_bytes(err, "big"), res


if __name__ == "__main__":
    host, cfile = sys.argv[1], sys.argv[2]
    community = open(cfile).read().strip()
    try:
        err, res = get(host, community, sys.argv[3:])
    except socket.timeout:
        print("no answer (timeout)")
        sys.exit(2)
    print("error-status", err)
    for oid, v in res:
        print(oid, "=", v)
