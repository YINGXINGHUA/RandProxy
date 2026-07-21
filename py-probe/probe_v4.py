#!/usr/bin/env python3
"""
Probe v4: test HTTPS against MULTIPLE targets (CHN-friendly).
Hypothesis: earlier TLS failures were because httpbin.org is unreachable from these proxies.
"""

import socket
import ssl
import time
import urllib.request
import json

API_URL = "https://proxy.scdn.io/api/get_proxy.php"
TIMEOUT = 10

# Targets: mix of Chinese and international
TARGETS = [
    ("www.baidu.com", 443, "Baidu (CN)"),
    ("www.qq.com", 443, "Tencent (CN)"),
    ("www.xxx.com", 443, "Literouter (target)"),
    ("httpbin.org", 443, "httpbin (US)"),
]


def fetch(protocol="https", count=20):
    url = f"{API_URL}?protocol={protocol}&count={count}"
    try:
        resp = urllib.request.urlopen(url, timeout=10)
        data = json.loads(resp.read())
        if data.get("code") == 200:
            return data["data"]["proxies"]
    except Exception as e:
        print(f"[api] {e}")
    return []


def test_proxy_full(addr, target_host, target_port, label):
    """Test: CONNECT + TLS handshake + GET. Return dict."""
    r = {"ok": False, "connect_ms": None, "tls_ms": None, "error": None}
    try:
        ip, port_str = addr.split(":")
        port = int(port_str)
    except Exception as e:
        r["error"] = f"parse: {e}"
        return r

    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(TIMEOUT)
    try:
        sock.connect((ip, port))
        t0 = time.time()
        cr = f"CONNECT {target_host}:{target_port} HTTP/1.1\r\nHost: {target_host}:{target_port}\r\n\r\n"
        sock.sendall(cr.encode())
        resp = b""
        while True:
            chunk = sock.recv(4096)
            resp += chunk
            if b"\r\n\r\n" in resp:
                break
        r["connect_ms"] = round((time.time() - t0) * 1000)

        resp_str = resp.decode(errors="replace")
        if "200" not in resp_str:
            r["error"] = f"CONNECT refused: {resp_str[:60]}"
            sock.close()
            return r

        # TLS handshake
        t0 = time.time()
        ctx = ssl.create_default_context()
        ctx.check_hostname = False
        ctx.verify_mode = ssl.CERT_NONE
        tls = ctx.wrap_socket(sock, server_hostname=target_host)
        r["tls_ms"] = round((time.time() - t0) * 1000)

        # GET
        tls.sendall(f"GET / HTTP/1.1\r\nHost: {target_host}\r\nConnection: close\r\n\r\n".encode())
        data = tls.recv(4096)
        r["ok"] = True
        tls.close()
    except Exception as e:
        r["error"] = f"{type(e).__name__}: {str(e)[:60]}"
        try:
            sock.close()
        except Exception:
            pass
    return r


def main():
    print("=== Fetching proxies (protocol=https x20) ===\n")
    addrs = fetch(protocol="https", count=20)
    if not addrs:
        print("No proxies returned.")
        return

    print(f"Got {len(addrs)} proxies. Testing each against {len(TARGETS)} targets...\n")

    # Print header
    header = f"  {'Proxy':<22}"
    for _, _, label in TARGETS:
        header += f" {label:>18}"
    print(header)

    sep = f"  {'─'*22}"
    for _, _, label in TARGETS:
        sep += f" {'─'*18}"
    print(sep)

    # Per-proxy, per-target
    stats = {label: {"ok": 0, "fail": 0} for _, _, label in TARGETS}

    for addr in addrs:
        line = f"  {addr:<22}"
        for host, port, label in TARGETS:
            r = test_proxy_full(addr, host, port, label)
            if r["ok"]:
                stats[label]["ok"] += 1
                cell = f"✅{r['connect_ms']}ms"
            else:
                stats[label]["fail"] += 1
                err_short = (r["error"] or "?").split(":")[0][:10]
                cell = f"❌{err_short}"
            line += f" {cell:>18}"
        print(line)

    print(f"\n=== Summary ===")
    for _, _, label in TARGETS:
        ok = stats[label]["ok"]
        total = ok + stats[label]["fail"]
        print(f"  {label:>22}: {ok}/{total} ({ok/total*100:.0f}%)")


if __name__ == "__main__":
    main()