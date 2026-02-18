# pyright: reportImplicitRelativeImport=false

import asyncio
import logging
import time
from collections.abc import Awaitable, Callable

from protocol import (
    MSG_I_HAVE,
    MSG_MESH_HELLO,
    MSG_RELAY_OPEN,
    MSG_WHO_HAS,
    generate_id,
    i_have,
    mesh_hello,
    pipe,
    read_msg,
    relay_open,
    who_has,
    write_msg,
)


log = logging.getLogger("mesh")


class MeshManager:
    def __init__(
        self,
        node_id: str,
        local_map: dict[str, object],
        get_work_conn_cb: Callable[
            [str],
            Awaitable[tuple[asyncio.StreamReader, asyncio.StreamWriter]],
        ],
        peer_addresses: dict[str, tuple[str, int]],
        control_port: int = 0,
    ):
        self.node_id = node_id
        self.local_map = local_map
        self.get_work_conn_cb = get_work_conn_cb
        self.peers: dict[str, tuple[asyncio.StreamReader, asyncio.StreamWriter]] = {}
        self.peer_addresses: dict[str, tuple[str, int]] = peer_addresses
        self.control_port = control_port
        self.seen_messages: dict[str, float] = {}  # msg_id → monotonic timestamp
        self.pending_searches: dict[str, asyncio.Future[dict[str, object]]] = {}
        self._inflight: dict[str, asyncio.Future[dict[str, object] | None]] = {}  # hostname → shared future

    def _mark_seen(self, msg_id: str) -> None:
        now = time.monotonic()
        if len(self.seen_messages) > 1000:
            cutoff = now - 30.0
            self.seen_messages = {k: v for k, v in self.seen_messages.items() if v > cutoff}
        self.seen_messages[msg_id] = now

    async def connect_to_peers(self, peer_specs: list[str]) -> None:
        for spec in peer_specs:
            try:
                host, port_raw = spec.rsplit(":", 1)
                port = int(port_raw)
            except ValueError:
                log.error("[%s] Invalid peer spec: %s", self.node_id, spec)
                continue

            try:
                reader, writer = await asyncio.open_connection(host, port)
                await write_msg(writer, MSG_MESH_HELLO, mesh_hello(self.node_id, [], self.control_port))
                msg_type, body = await read_msg(reader)

                if msg_type != MSG_MESH_HELLO or not body:
                    log.error("[mesh-%s] Invalid mesh hello from %s", self.node_id, spec)
                    writer.close()
                    await writer.wait_closed()
                    continue

                remote_node_id = body.get("node_id")
                if not remote_node_id:
                    log.error("[mesh-%s] Mesh hello missing node_id from %s", self.node_id, spec)
                    writer.close()
                    await writer.wait_closed()
                    continue

                self.peers[remote_node_id] = (reader, writer)
                self.peer_addresses[remote_node_id] = (host, port)
                asyncio.create_task(self._peer_loop(remote_node_id, reader, writer))
                log.info("[mesh-%s] Connected to peer %s at %s:%s", self.node_id, remote_node_id, host, port)
            except Exception as exc:
                log.error("[mesh-%s] Failed connecting to peer %s: %s", self.node_id, spec, exc)

    async def handle_peer(
        self,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter,
        hello_body: dict[str, object],
    ) -> None:
        remote_node_id = (hello_body or {}).get("node_id")
        if not isinstance(remote_node_id, str) or not remote_node_id:
            log.error("[mesh-%s] Inbound mesh hello missing node_id", self.node_id)
            return

        await write_msg(writer, MSG_MESH_HELLO, mesh_hello(self.node_id, [], self.control_port))
        self.peers[remote_node_id] = (reader, writer)

        remote_port = (hello_body or {}).get("control_port", 0)
        addr = writer.get_extra_info("peername")
        if isinstance(remote_port, int) and remote_port > 0 and isinstance(addr, tuple) and len(addr) >= 2:
            self.peer_addresses[remote_node_id] = (str(addr[0]), remote_port)

        asyncio.create_task(self._peer_loop(remote_node_id, reader, writer))
        log.info("[mesh-%s] Accepted peer %s", self.node_id, remote_node_id)

    async def _peer_loop(
        self,
        peer_id: str,
        reader: asyncio.StreamReader,
        _writer: asyncio.StreamWriter,
    ) -> None:
        try:
            while True:
                msg_type, body = await read_msg(reader)
                if msg_type is None:
                    break
                if body is None:
                    continue
                if msg_type == MSG_WHO_HAS:
                    await self._handle_who_has(peer_id, body)
                elif msg_type == MSG_I_HAVE:
                    await self._handle_i_have(peer_id, body)
                else:
                    log.warning("Unknown mesh msg type: %s", chr(msg_type))
        except Exception as exc:
            log.error("Peer loop error for %s: %s", peer_id, exc)
        finally:
            self.peers.pop(peer_id, None)
            log.info("Peer %s disconnected", peer_id)

    async def _handle_who_has(self, sender_id: str, body: dict[str, object]) -> None:
        msg_id = body.get("msg_id")
        hostname = body.get("hostname")
        ttl = body.get("ttl", 0)
        path = body.get("path", [])

        if not isinstance(msg_id, str) or not isinstance(hostname, str):
            return
        if not isinstance(ttl, int):
            return
        if not isinstance(path, list):
            return

        clean_path = [node for node in path if isinstance(node, str)]

        if msg_id in self.seen_messages:
            return
        self._mark_seen(msg_id)

        if ttl <= 0:
            return

        if hostname in self.local_map:
            response = i_have(msg_id, hostname, self.node_id, clean_path)
            if sender_id in self.peers:
                _, writer = self.peers[sender_id]
                await write_msg(writer, MSG_I_HAVE, response)
                log.info("IHave %s -> %s", hostname, sender_id)
            return

        forward_body = who_has(msg_id, hostname, ttl - 1, clean_path + [self.node_id])
        for pid, (_, peer_writer) in list(self.peers.items()):
            if pid != sender_id:
                await write_msg(peer_writer, MSG_WHO_HAS, forward_body)
                log.debug("Forward WhoHas %s -> %s", hostname, pid)

    async def _handle_i_have(self, sender_id: str, body: dict[str, object]) -> None:
        _ = sender_id
        msg_id = body.get("msg_id")
        if not isinstance(msg_id, str):
            return

        if msg_id in self.pending_searches:
            future = self.pending_searches.pop(msg_id)
            if not future.done():
                future.set_result(body)
            log.info("IHave resolved: %s at %s", body.get("hostname"), body.get("node_id"))
            return

        path = body.get("path", [])
        if not isinstance(path, list) or not path:
            return

        clean_path = [node for node in path if isinstance(node, str)]
        if not clean_path:
            return

        my_idx = None
        for idx, node in enumerate(clean_path):
            if node == self.node_id:
                my_idx = idx
                break

        if my_idx is not None and my_idx > 0:
            prev_node = clean_path[my_idx - 1]
            if prev_node in self.peers:
                _, writer = self.peers[prev_node]
                await write_msg(writer, MSG_I_HAVE, body)
                log.debug("Forward IHave %s -> %s", body.get("hostname"), prev_node)

    async def find_service(self, hostname: str) -> dict[str, object] | None:
        if not self.peers:
            return None

        if hostname in self._inflight:
            return await self._inflight[hostname]

        loop = asyncio.get_event_loop()
        shared: asyncio.Future[dict[str, object] | None] = loop.create_future()
        self._inflight[hostname] = shared
        try:
            result = await self._broadcast(hostname)
            if not shared.done():
                shared.set_result(result)
            return result
        except Exception:
            if not shared.done():
                shared.set_result(None)
            return None
        finally:
            self._inflight.pop(hostname, None)

    async def _broadcast(self, hostname: str) -> dict[str, object] | None:
        msg_id = generate_id()
        future = asyncio.get_event_loop().create_future()
        self.pending_searches[msg_id] = future
        self._mark_seen(msg_id)

        body = who_has(msg_id, hostname, 5, [self.node_id])
        for _, peer_writer in self.peers.values():
            await write_msg(peer_writer, MSG_WHO_HAS, body)

        try:
            result = await asyncio.wait_for(future, timeout=3.0)
            return result
        except asyncio.TimeoutError:
            self.pending_searches.pop(msg_id, None)
            log.warning("No service found for %s (timeout)", hostname)
            return None

    async def open_relay(
        self,
        hostname: str,
        target_node_id: str,
        path: list[str],
    ) -> tuple[asyncio.StreamReader, asyncio.StreamWriter]:
        _ = target_node_id
        if not path:
            raise ValueError("Relay path is empty")

        next_hop = path[0]
        remaining = path[1:]
        if next_hop not in self.peer_addresses:
            raise KeyError(f"Unknown next hop: {next_hop}")

        host, port = self.peer_addresses[next_hop]
        reader, writer = await asyncio.open_connection(host, int(port))
        relay_id = generate_id()
        await write_msg(writer, MSG_RELAY_OPEN, relay_open(relay_id, hostname, remaining))
        return (reader, writer)

    async def handle_relay_open(
        self,
        reader: asyncio.StreamReader,
        writer: asyncio.StreamWriter,
        body: dict[str, object],
    ) -> None:
        hostname = body.get("hostname")
        next_hops = body.get("next_hops", [])
        relay_id = body.get("relay_id")

        if not isinstance(hostname, str) or not isinstance(relay_id, str):
            log.error("[mesh-%s] Invalid relay open payload", self.node_id)
            writer.close()
            await writer.wait_closed()
            return

        if not isinstance(next_hops, list):
            log.error("[mesh-%s] Invalid next_hops in relay open", self.node_id)
            writer.close()
            await writer.wait_closed()
            return

        clean_hops = [hop for hop in next_hops if isinstance(hop, str)]

        if not clean_hops:
            log.info("[mesh-%s] Relay %s for %s: final", self.node_id, relay_id, hostname)
            work_reader, work_writer = await self.get_work_conn_cb(hostname)
            await asyncio.gather(pipe(reader, work_writer), pipe(work_reader, writer))
            return

        next_hop = clean_hops[0]
        remaining = clean_hops[1:]
        if next_hop not in self.peer_addresses:
            log.error("[mesh-%s] Unknown next hop for relay %s: %s", self.node_id, relay_id, next_hop)
            writer.close()
            await writer.wait_closed()
            return

        host, port = self.peer_addresses[next_hop]
        log.info("[mesh-%s] Relay %s for %s: -> %s", self.node_id, relay_id, hostname, next_hop)
        nr, nw = await asyncio.open_connection(host, int(port))
        await write_msg(nw, MSG_RELAY_OPEN, relay_open(relay_id, hostname, remaining))
        await asyncio.gather(pipe(reader, nw), pipe(nr, writer))
