"""
protocol.py — TLV+JSON protocol layer for the drp POC.

Frame format: [Type: 1 byte][Length: 4 bytes big-endian][Body: JSON UTF-8]
"""

import asyncio
import json
import logging
import struct
import uuid

log = logging.getLogger("protocol")

# ---------------------------------------------------------------------------
# Message type constants (ASCII byte values)
# ---------------------------------------------------------------------------
MSG_LOGIN          = ord('L')  # 0x4C — Client → Server: auth request
MSG_LOGIN_RESP     = ord('l')  # 0x6C — Server → Client: auth response
MSG_NEW_PROXY      = ord('P')  # 0x50 — Client → Server: register service
MSG_NEW_PROXY_RESP = ord('p')  # 0x70 — Server → Client: registration result
MSG_REQ_WORK_CONN  = ord('R')  # 0x52 — Server → Client: request work connection
MSG_NEW_WORK_CONN  = ord('W')  # 0x57 — Client → Server (first byte on control port)
MSG_START_WORK_CONN = ord('S') # 0x53 — Server → Client: start relay
MSG_PING           = ord('H')  # 0x48 — Heartbeat
MSG_PONG           = ord('h')  # 0x68 — Heartbeat response
MSG_MESH_HELLO     = ord('M')  # 0x4D — Server ↔ Server: mesh init
MSG_WHO_HAS        = ord('F')  # 0x46 — Server → Server: broadcast search
MSG_I_HAVE         = ord('I')  # 0x49 — Server → Server: search response
MSG_RELAY_OPEN     = ord('O')  # 0x4F — Server → Server: relay data connection

# ---------------------------------------------------------------------------
# Body constructor functions
# ---------------------------------------------------------------------------

def login(alias: str) -> dict:
    return {"alias": alias}


def login_resp(ok: bool, message: str = "") -> dict:
    return {"ok": ok, "message": message}


def new_proxy(alias: str, hostname: str) -> dict:
    return {"alias": alias, "hostname": hostname}


def new_proxy_resp(ok: bool, message: str = "") -> dict:
    return {"ok": ok, "message": message}


def req_work_conn() -> dict:
    return {}


def new_work_conn(alias: str) -> dict:
    return {"alias": alias}


def start_work_conn(hostname: str) -> dict:
    return {"hostname": hostname}


def mesh_hello(node_id: str, peers: list | None = None, control_port: int = 0) -> dict:
    return {"node_id": node_id, "peers": peers or [], "control_port": control_port}


def who_has(msg_id: str, hostname: str, ttl: int, path: list) -> dict:
    return {"msg_id": msg_id, "hostname": hostname, "ttl": ttl, "path": path}


def i_have(msg_id: str, hostname: str, node_id: str, path: list) -> dict:
    return {"msg_id": msg_id, "hostname": hostname, "node_id": node_id, "path": path}


def relay_open(relay_id: str, hostname: str, next_hops: list) -> dict:
    return {"relay_id": relay_id, "hostname": hostname, "next_hops": next_hops}


# ---------------------------------------------------------------------------
# ID generator
# ---------------------------------------------------------------------------

def generate_id() -> str:
    return uuid.uuid4().hex[:12]


# ---------------------------------------------------------------------------
# TLV read / write
# ---------------------------------------------------------------------------

async def read_msg(reader: asyncio.StreamReader) -> tuple[int | None, dict | None]:
    """Read one TLV frame from *reader*.

    Returns ``(msg_type, body_dict)`` on success, or ``(None, None)`` on EOF.
    """
    try:
        header = await reader.readexactly(5)
    except asyncio.IncompleteReadError:
        return (None, None)

    msg_type = header[0]
    body_len = struct.unpack('>I', header[1:5])[0]

    if body_len > 0:
        try:
            body_bytes = await reader.readexactly(body_len)
        except asyncio.IncompleteReadError:
            return (None, None)
        body = json.loads(body_bytes)
    else:
        body = {}

    return (msg_type, body)


async def write_msg(writer: asyncio.StreamWriter, msg_type: int, body: dict) -> None:
    """Encode *body* as JSON and write a TLV frame to *writer*, then drain."""
    body_bytes = json.dumps(body).encode('utf-8')
    header = struct.pack('>cI', bytes([msg_type]), len(body_bytes))
    writer.write(header + body_bytes)
    await writer.drain()


# ---------------------------------------------------------------------------
# Relay helpers
# ---------------------------------------------------------------------------

async def pipe(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    """Bidirectional-relay helper: forward all bytes from *reader* to *writer*."""
    try:
        while True:
            data = await reader.read(65536)
            if not data:
                break
            writer.write(data)
            await writer.drain()
    finally:
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass


# ---------------------------------------------------------------------------
# HTTP Host header extraction
# ---------------------------------------------------------------------------

def extract_host(raw_bytes: bytes) -> str | None:
    """Return the Host header value from a raw HTTP request, or None."""
    try:
        lines = raw_bytes.split(b'\r\n')
        for line in lines:
            decoded = line.decode('utf-8', errors='replace')
            if decoded.lower().startswith('host:'):
                value = decoded[5:].strip()
                # Strip port — but only for non-IPv6 addresses
                if value.startswith('['):
                    # IPv6 literal — leave as-is
                    return value
                if ':' in value:
                    value = value.split(':')[0]
                return value
    except Exception:
        pass
    return None


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------

__all__ = [
    # Constants
    "MSG_LOGIN",
    "MSG_LOGIN_RESP",
    "MSG_NEW_PROXY",
    "MSG_NEW_PROXY_RESP",
    "MSG_REQ_WORK_CONN",
    "MSG_NEW_WORK_CONN",
    "MSG_START_WORK_CONN",
    "MSG_PING",
    "MSG_PONG",
    "MSG_MESH_HELLO",
    "MSG_WHO_HAS",
    "MSG_I_HAVE",
    "MSG_RELAY_OPEN",
    # Body constructors
    "login",
    "login_resp",
    "new_proxy",
    "new_proxy_resp",
    "req_work_conn",
    "new_work_conn",
    "start_work_conn",
    "mesh_hello",
    "who_has",
    "i_have",
    "relay_open",
    # Helpers
    "generate_id",
    "read_msg",
    "write_msg",
    "pipe",
    "extract_host",
    # Logger
    "log",
]
