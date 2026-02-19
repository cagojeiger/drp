#!/usr/bin/env python3
"""
drpc.py — drp client that connects to a server, registers a service, and provides work connections.

Usage:
    python drpc.py --server localhost:9001 --alias myapp --hostname myapp.example.com --local localhost:5000
"""

import argparse
import asyncio
import logging
import signal
import sys

from protocol import (
    MSG_LOGIN,
    MSG_LOGIN_RESP,
    MSG_NEW_PROXY,
    MSG_NEW_PROXY_RESP,
    MSG_REQ_WORK_CONN,
    MSG_NEW_WORK_CONN,
    MSG_START_WORK_CONN,
    login,
    new_proxy,
    new_work_conn,
    read_msg,
    write_msg,
    pipe,
)

log = logging.getLogger("drpc")


def parse_host_port(addr: str) -> tuple[str, int]:
    """Parse 'HOST:PORT' string into (host, port) tuple."""
    if ':' not in addr:
        raise ValueError(f"Invalid address format: {addr}. Expected HOST:PORT")
    host, port_str = addr.rsplit(':', 1)
    try:
        port = int(port_str)
    except ValueError:
        raise ValueError(f"Invalid port: {port_str}")
    return (host, port)


async def handle_work_conn(server_host: str, server_port: int, alias: str, local_host: str, local_port: int):
    """Handle a single work connection request.
    
    1. Open NEW TCP to server
    2. Send NewWorkConn as first message
    3. Read StartWorkConn
    4. Connect to local service
    5. Bidirectional relay: server ↔ local
    """
    work_reader = None
    work_writer = None
    local_reader = None
    local_writer = None
    
    try:
        # Open NEW TCP to server
        work_reader, work_writer = await asyncio.open_connection(server_host, server_port)
        log.debug(f"Opened work connection to {server_host}:{server_port}")
        
        # Send NewWorkConn as first message (this is how server identifies this as a work conn)
        await write_msg(work_writer, MSG_NEW_WORK_CONN, new_work_conn(alias))
        
        # Read StartWorkConn
        msg_type, body = await read_msg(work_reader)
        if msg_type is None:
            log.error("Work connection closed before StartWorkConn")
            return
        
        if msg_type != MSG_START_WORK_CONN:
            log.error(f"Expected StartWorkConn, got message type {msg_type}")
            return
        
        hostname = body.get("hostname", "unknown")
        log.info(f"Work conn for {hostname}")
        
        # Connect to local service
        try:
            local_reader, local_writer = await asyncio.open_connection(local_host, local_port)
            log.debug(f"Connected to local service {local_host}:{local_port}")
        except Exception as e:
            log.error(f"Failed to connect to local service {local_host}:{local_port}: {e}")
            return
        
        # Bidirectional relay: server ↔ local
        await asyncio.gather(
            pipe(work_reader, local_writer),  # server → local
            pipe(local_reader, work_writer),  # local → server
        )
        
        log.debug(f"Work conn for {hostname} closed")
        
    except Exception as e:
        log.error(f"Error in work connection: {e}")
    finally:
        # Clean up connections
        if work_writer:
            try:
                work_writer.close()
                await work_writer.wait_closed()
            except Exception:
                pass
        if local_writer:
            try:
                local_writer.close()
                await local_writer.wait_closed()
            except Exception:
                pass


async def main(args):
    """Main client loop: connect, login, register proxy, handle work connection requests."""
    server_host, server_port = parse_host_port(args.server)
    local_host, local_port = parse_host_port(args.local)
    
    control_reader = None
    control_writer = None
    
    try:
        # Control connection
        control_reader, control_writer = await asyncio.open_connection(server_host, server_port)
        log.info(f"Connected to {args.server}")
        
        # Login
        await write_msg(control_writer, MSG_LOGIN, login(args.alias))
        msg_type, body = await read_msg(control_reader)
        
        if msg_type is None:
            log.error("Connection closed during login")
            return 1
        
        if msg_type != MSG_LOGIN_RESP:
            log.error(f"Expected LoginResp, got message type {msg_type}")
            return 1
        
        if not body.get("ok"):
            log.error(f"Login failed: {body.get('message', 'unknown error')}")
            return 1
        
        log.info("Login OK")
        
        # Register proxy
        await write_msg(control_writer, MSG_NEW_PROXY, new_proxy(args.alias, args.hostname))
        msg_type, body = await read_msg(control_reader)
        
        if msg_type is None:
            log.error("Connection closed during proxy registration")
            return 1
        
        if msg_type != MSG_NEW_PROXY_RESP:
            log.error(f"Expected NewProxyResp, got message type {msg_type}")
            return 1
        
        if not body.get("ok"):
            log.error(f"NewProxy failed: {body.get('message', 'unknown error')}")
            return 1
        
        log.info(f"Registered {args.alias} → {args.hostname}")
        
        # Control loop — listen for ReqWorkConn
        log.info("Listening for work connection requests...")
        while True:
            msg_type, body = await read_msg(control_reader)
            
            if msg_type is None:
                log.warning("Control connection closed")
                break
            
            if msg_type == MSG_REQ_WORK_CONN:
                log.debug("Received ReqWorkConn")
                # Spawn work connection handler as background task
                asyncio.create_task(
                    handle_work_conn(server_host, server_port, args.alias, local_host, local_port)
                )
            else:
                log.warning(f"Unexpected message type on control connection: {msg_type}")
        
        return 0
        
    except KeyboardInterrupt:
        log.info("Shutting down...")
        return 0
    except Exception as e:
        log.error(f"Fatal error: {e}")
        return 1
    finally:
        # Clean up control connection
        if control_writer:
            try:
                control_writer.close()
                await control_writer.wait_closed()
            except Exception:
                pass


def parse_args():
    """Parse command-line arguments."""
    parser = argparse.ArgumentParser(
        description="drp client — connect to drps server and expose local service",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s --server localhost:9001 --alias myapp --hostname myapp.example.com --local localhost:5000
  %(prog)s --server lb.example.com:9000 --alias api --hostname api.example.com --local 127.0.0.1:8080
        """
    )
    
    parser.add_argument(
        '--server',
        required=True,
        metavar='HOST:PORT',
        help='drps server address (e.g., localhost:9001)'
    )
    
    parser.add_argument(
        '--alias',
        required=True,
        metavar='NAME',
        help='service alias (e.g., myapp)'
    )
    
    parser.add_argument(
        '--hostname',
        required=True,
        metavar='FQDN',
        help='public hostname for the service (e.g., myapp.example.com)'
    )
    
    parser.add_argument(
        '--local',
        required=True,
        metavar='HOST:PORT',
        help='local service address (e.g., localhost:5000)'
    )
    
    parser.add_argument(
        '--verbose',
        '-v',
        action='store_true',
        help='enable verbose logging (DEBUG level)'
    )
    
    return parser.parse_args()


if __name__ == "__main__":
    args = parse_args()
    
    # Configure logging
    log_level = logging.DEBUG if args.verbose else logging.INFO
    logging.basicConfig(
        level=log_level,
        format="[drpc] %(message)s"
    )
    
    # Run main loop
    try:
        exit_code = asyncio.run(main(args))
        sys.exit(exit_code)
    except KeyboardInterrupt:
        log.info("Interrupted")
        sys.exit(0)
