#!/usr/bin/env python3
"""
Probe v3: bare-metal CONNECT test on all HTTP-passing proxies.
Tests: CONNECT → TLS handshake → GET request.
"""

import socket
import ssl
import time
import urllib.request
import json

API_URL = "https://proxy.scdn.io/api/get_proxy.php"
TARGET_HOST = "httpbin.org"
TARGET_PORT = 443
TIMEOUT = 8


def fetch(protocol="all", count=20):
    url = f"{API_URL}?protocol={protocol}&count={count}"
    try:
        resp = urllib.request.urlopen(url, timeout=10)
        data = json.loads(resp.read())
        if data.get("code") == 200:
            return data["data"]["proxies"]
    except Exception as e:
        print(f"[api] {e}")
    return []


def raw_http_test(addr):
    """Quick plain HTTP test using raw socket."""
    try:
        ip, port_str = addr.split(":")
        port = int(port_str)
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(TIMEOUT)
        sock.connect((ip, port))
        req = (
            f"GET http://{TARGET_HOST}/ip HTTP/1.1\r\n"
            f"Host: {TARGET_HOST}\r\n"
            f"Connection: close\r\n\r\n"
        )
        sock.sendall(req.encode())
        data = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            data += chunk
        sock.close()
        raw = data.decode(errors="replace")
        if "200" in raw[:100]:
            return True
    except Exception:
        pass
    return False


def raw_connect_test(addr):
    """Test HTTPS CONNECT tunnel by raw socket."""
    result = {"http_ok": False, "connect_ok": False, "tls_ok": False,
              "connect_ms": None, "tls_ms": None}

    try:
        ip, port_str = addr.split(":")
        port = int(port_str)
    except Exception:
        return result

    # --- Step 1: HTTP via proxy ---
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(TIMEOUT)
    try:
        sock.connect((ip, port))
        req = (
            f"GET http://{TARGET_HOST}/ip HTTP/1.1\r\n"
            f"Host: {TARGET_HOST}\r\n"
            f"Connection: close\r\n\r\n"
        )
        sock.sendall(req.encode())
        data = b""
        while True:
            chunk = sock.recv(4096)
            if not chunk:
                break
            data += chunk
        sock.close()
        raw = data.decode(errors="replace")
        if "200" in raw[:100]:
            result["http_ok"] = True
        else:
            return result
    except Exception:
        sock.close()
        return result

    # --- Step 2: CONNECT tunnel ---
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(TIMEOUT)
    try:
        sock.connect((ip, port))
        t0 = time.time()
        connect_req = f"CONNECT {TARGET_HOST}:{TARGET_PORT} HTTP/1.1\r\nHost: {TARGET_HOST}:{TARGET_PORT}\r\n\r\n"
        sock.sendall(connect_req.encode())
        resp = b""
        while True:
            chunk = sock.recv(4096)
            resp += chunk
            if b"\r\n\r\n" in resp:
                break
        t_connect = time.time() - t0
        result["connect_ms"] = round(t_connect * 1000)

        resp_str = resp.decode(errors="replace")
        if "200" in resp_str:
            result["connect_ok"] = True
        else:
            sock.close()
            return result
    except Exception:
        sock.close()
        return result

    # --- Step 3: TLS handshake ---
    try:
        t0 = time.time()
        ctx = ssl.create_default_context()
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        tls = ctx.wrap_socket(sock, server_hostname=TARGET_HOST)
        result["tls_ms"] = round((time.time() - t0) * 1000)
        result["tls_ok"] = True
        tls.close()
    except Exception:
        sock.close()
        return result

    sock.close()
    return result


def main():
    print("=== Step 1: Harvest proxies (protocol=https x20) ===\n")
    addrs = fetch(protocol="https", count=20)
    if not addrs:
        print("No proxies returned.")
        return

    print(f"Got {len(addrs)} proxies. Step 2: testing...\n")

    print(f"  {'Proxy':<22} {'HTTP':>6} {'CONNECT':>9} {'TLS':>5} {'Note'}")
    print(f"  {'─'*22} {'─'*6} {'─'*9} {'─'*5} {'─'*20}")

    stats = {"http": 0, "connect": 0, "tls": 0}
    for addr in addrs:
        r = raw_connect_test(addr)

        http_str = "✅" if r["http_ok"] else "❌"
        conn_str = f"{r['connect_ms']}ms" if r["connect_ok"] else "❌"
        tls_str = f"{r['tls_ms']}ms" if r["tls_ok"] else "❌"

        note = ""
        if r["http_ok"]:
            stats["http"] += 1
        if r["connect_ok"]:
            stats["connect"] += 1
            note = "CONNECT OK"
        if r["tls_ok"]:
            stats["tls"] += 1
            note = "✅ TLS OK"

        print(f"  {addr:<22} {http_str:>6} {conn_str:>9} {tls_str:>5}  {note}")

    print(f"\n=== Results ===")
    print(f"  HTTP  usable:   {stats['http']}/{len(addrs)}")
    print(f"  CONNECT ok:     {stats['connect']}/{len(addrs)}")
    print(f"  TLS handshake:  {stats['tls']}/{len(addrs)} (truly HTTPS-ready)")


if __name__ == "__main__":
    main()