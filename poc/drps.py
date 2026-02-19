#!/usr/bin/env python3
# pyright: reportImplicitRelativeImport=false
"""
drps.py — drp server: HTTP listener + control port dispatcher + mesh.

Usage:
    python drps.py --node-id A --http-port 8001 --control-port 9001
    python drps.py --node-id B --http-port 8002 --control-port 9002 --peers localhost:9001
    python drps.py --node-id C --http-port 8003 --control-port 9003 --peers localhost:9002
"""

import argparse
import asyncio
import logging
import sys

from protocol import (
    MSG_LOGIN,
    MSG_LOGIN_RESP,
    MSG_NEW_PROXY,
    MSG_NEW_PROXY_RESP,
    MSG_REQ_WORK_CONN,
    MSG_NEW_WORK_CONN,
    MSG_START_WORK_CONN,
    MSG_MESH_HELLO,
    MSG_RELAY_OPEN,
    login_resp,
    new_proxy_resp,
    req_work_conn,
    start_work_conn,
    read_msg,
    write_msg,
    pipe,
    extract_host,
)
from mesh import MeshManager

log = logging.getLogger("drps")

# ---------------------------------------------------------------------------
# Shared state: hostname → {alias, ctrl_writer, work_queue}
# ---------------------------------------------------------------------------
local_map: dict[str, dict] = {}


# ---------------------------------------------------------------------------
# HTTP listener
# ---------------------------------------------------------------------------

async def handle_http(
    reader: asyncio.StreamReader,
    writer: asyncio.StreamWriter,
    mesh: MeshManager,
) -> None:
    """Incoming HTTP → extract Host → local work-conn or mesh relay."""
    try:
        # Read HTTP headers (until \r\n\r\n)
        buf = b""
        while b"\r\n\r\n" not in buf:
            chunk = await reader.read(4096)
            if not chunk:
                return
            buf += chunk
            if len(buf) > 65536:
                writer.write(b"HTTP/1.1 431 Request Header Fields Too Large\r\n\r\n")
                await writer.drain()
                return

        hostname = extract_host(buf)
        if not hostname:
            writer.write(b"HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n")
            await writer.drain()
            return

        log.info("HTTP %s", hostname)

        # --- local hit ---
        if hostname in local_map:
            entry = local_map[hostname]
            try:
                await write_msg(entry["ctrl_writer"], MSG_REQ_WORK_CONN, req_work_conn())
                wr, ww = await asyncio.wait_for(entry["work_queue"].get(), timeout=10.0)
                await write_msg(ww, MSG_START_WORK_CONN, start_work_conn(hostname))
                ww.write(buf)
                await ww.drain()
                await asyncio.gather(pipe(reader, ww), pipe(wr, writer))
            except asyncio.TimeoutError:
                log.error("Work conn timeout for %s", hostname)
                writer.write(b"HTTP/1.1 504 Gateway Timeout\r\n\r\n")
                await writer.drain()
            except Exception as e:
                log.error("Local relay error for %s: %s", hostname, e)
            return

        # --- mesh lookup ---
        result = await mesh.find_service(hostname)
        if result is None:
            log.warning("No service for %s", hostname)
            writer.write(b"HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n")
            await writer.drain()
            return

        target_node = str(result.get("node_id", ""))
        who_path = list(result.get("path", []))
        # relay path: skip self (path[0]), add target node at end
        relay_path = [str(n) for n in who_path[1:]] + [target_node]

        try:
            rr, rw = await mesh.open_relay(hostname, target_node, relay_path)
            rw.write(buf)
            await rw.drain()
            await asyncio.gather(pipe(reader, rw), pipe(rr, writer))
        except Exception as e:
            log.error("Relay error for %s: %s", hostname, e)
            writer.write(b"HTTP/1.1 502 Bad Gateway\r\n\r\n")
            await writer.drain()

    except Exception as e:
        log.error("HTTP handler error: %s", e)
    finally:
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass


# ---------------------------------------------------------------------------
# Control port dispatcher
# ---------------------------------------------------------------------------

async def handle_control(
    reader: asyncio.StreamReader,
    writer: asyncio.StreamWriter,
    mesh: MeshManager,
) -> None:
    """Read first TLV message and dispatch by type."""
    try:
        msg_type, body = await read_msg(reader)
        if msg_type is None:
            writer.close()
            await writer.wait_closed()
            return

        if body is None:
            writer.close()
            await writer.wait_closed()
            return

        if msg_type == MSG_LOGIN:
            await _client_session(reader, writer, body)
        elif msg_type == MSG_MESH_HELLO:
            await mesh.handle_peer(reader, writer, body)
        elif msg_type == MSG_NEW_WORK_CONN:
            await _accept_work_conn(reader, writer, body)
        elif msg_type == MSG_RELAY_OPEN:
            await mesh.handle_relay_open(reader, writer, body)
        else:
            log.warning("Unknown control msg type: 0x%02x", msg_type)
            writer.close()
            await writer.wait_closed()
    except Exception as e:
        log.error("Control handler error: %s", e)


# ---------------------------------------------------------------------------
# Client session (Login → NewProxy → control loop)
# ---------------------------------------------------------------------------

async def _client_session(
    reader: asyncio.StreamReader,
    writer: asyncio.StreamWriter,
    login_body: dict,
) -> None:
    alias = login_body.get("alias", "?")
    await write_msg(writer, MSG_LOGIN_RESP, login_resp(True, "ok"))
    log.info("Client %s logged in", alias)

    # Expect NewProxy
    msg_type, body = await read_msg(reader)
    if msg_type != MSG_NEW_PROXY or not body:
        log.error("Expected NewProxy from %s", alias)
        return

    hostname = body.get("hostname")
    if not hostname:
        await write_msg(writer, MSG_NEW_PROXY_RESP, new_proxy_resp(False, "missing hostname"))
        return

    work_queue: asyncio.Queue = asyncio.Queue()
    local_map[hostname] = {
        "alias": body.get("alias", alias),
        "ctrl_writer": writer,
        "work_queue": work_queue,
    }

    await write_msg(writer, MSG_NEW_PROXY_RESP, new_proxy_resp(True, "ok"))
    log.info("Registered %s → %s", alias, hostname)

    # Keep control connection alive
    try:
        while True:
            msg_type, _ = await read_msg(reader)
            if msg_type is None:
                break
    except Exception:
        pass
    finally:
        local_map.pop(hostname, None)
        try:
            writer.close()
            await writer.wait_closed()
        except Exception:
            pass
        log.info("Client %s (%s) disconnected", alias, hostname)


# ---------------------------------------------------------------------------
# Work connection acceptance
# ---------------------------------------------------------------------------

async def _accept_work_conn(
    reader: asyncio.StreamReader,
    writer: asyncio.StreamWriter,
    body: dict,
) -> None:
    """Put incoming work conn into the matching service queue."""
    alias = body.get("alias", "?")
    for hostname, entry in local_map.items():
        if entry["alias"] == alias:
            await entry["work_queue"].put((reader, writer))
            log.debug("Work conn queued for %s (%s)", alias, hostname)
            return

    log.error("No service for alias %s", alias)
    writer.close()
    await writer.wait_closed()


# ---------------------------------------------------------------------------
# Mesh callback: get work conn for relay final hop
# ---------------------------------------------------------------------------

async def _get_work_conn(hostname: str) -> tuple[asyncio.StreamReader, asyncio.StreamWriter]:
    """Called by mesh at relay final hop — request work conn from local drpc."""
    if hostname not in local_map:
        raise KeyError(f"No local service for {hostname}")

    entry = local_map[hostname]
    await write_msg(entry["ctrl_writer"], MSG_REQ_WORK_CONN, req_work_conn())
    wr, ww = await asyncio.wait_for(entry["work_queue"].get(), timeout=10.0)
    await write_msg(ww, MSG_START_WORK_CONN, start_work_conn(hostname))
    return (wr, ww)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

async def run(args: argparse.Namespace) -> None:
    peer_addresses: dict[str, tuple[str, int]] = {}
    mesh = MeshManager(
        node_id=args.node_id,
        local_map=local_map,
        get_work_conn_cb=_get_work_conn,
        peer_addresses=peer_addresses,
        control_port=args.control_port,
    )

    http_srv = await asyncio.start_server(
        lambda r, w: handle_http(r, w, mesh),
        "0.0.0.0", args.http_port,
    )
    log.info("HTTP on :%d", args.http_port)

    ctrl_srv = await asyncio.start_server(
        lambda r, w: handle_control(r, w, mesh),
        "0.0.0.0", args.control_port,
    )
    log.info("Control on :%d", args.control_port)

    if args.peers:
        peers = [p.strip() for p in args.peers.split(",") if p.strip()]
        await mesh.connect_to_peers(peers)

    log.info("Ready")

    async with http_srv, ctrl_srv:
        await asyncio.gather(http_srv.serve_forever(), ctrl_srv.serve_forever())


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="drps — distributed reverse proxy server",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s --node-id A --http-port 8001 --control-port 9001
  %(prog)s --node-id B --http-port 8002 --control-port 9002 --peers localhost:9001
  %(prog)s --node-id C --http-port 8003 --control-port 9003 --peers localhost:9002
        """,
    )
    p.add_argument("--node-id", required=True, help="Unique node identifier (e.g., A)")
    p.add_argument("--http-port", required=True, type=int, help="HTTP listener port")
    p.add_argument("--control-port", required=True, type=int, help="Control/mesh port")
    p.add_argument("--peers", default="", help="Comma-separated peer control addresses (e.g., localhost:9002)")
    p.add_argument("-v", "--verbose", action="store_true", help="DEBUG logging")
    return p.parse_args()


if __name__ == "__main__":
    args = parse_args()
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format=f"[drps-{args.node_id}] %(message)s",
    )
    try:
        asyncio.run(run(args))
    except KeyboardInterrupt:
        log.info("Shutdown")
