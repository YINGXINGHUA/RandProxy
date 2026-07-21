#!/usr/bin/env python3
"""
Probe: fetch proxies from proxy.scdn.io, test if they actually forward HTTP.
Zero dependencies (stdlib only).
"""

import json
import socket
import time
import urllib.request
from urllib.error import URLError

API_URL = "https://proxy.scdn.io/api/get_proxy.php"
TEST_URL = "http://httpbin.org/ip"
PER_PROXY_TIMEOUT = 4  # seconds
FETCH_TIMEOUT = 10


def fetch(protocol="all", count=5, country_code=None):
    params = f"?protocol={protocol}&count={count}"
    if country_code:
        params += f"&country_code={country_code}"
    url = API_URL + params
    try:
        resp = urllib.request.urlopen(url, timeout=FETCH_TIMEOUT)
        data = json.loads(resp.read())
        if data.get("code") == 200:
            return data["data"]["proxies"]
        else:
            print(f"  [api] unexpected: {data.get('message')}")
            return []
    except Exception as e:
        print(f"  [api] fetch error: {e}")
        return []


def test_proxy(addr):
    """Return (ok, latency, origin_ip) or (False, None, None)."""
    original_timeout = socket.getdefaulttimeout()
    socket.setdefaulttimeout(PER_PROXY_TIMEOUT)
    try:
        for scheme in ("http", "https"):
            proxy_hdl = urllib.request.ProxyHandler({
                "http": addr,
                "https": addr,
            })
            opener = urllib.request.build_opener(proxy_hdl)
            try:
                t0 = time.time()
                resp = opener.open(TEST_URL, timeout=PER_PROXY_TIMEOUT)
                elapsed = time.time() - t0
                body = resp.read().decode()
                origin = json.loads(body).get("origin", "?")
                return True, elapsed, origin
            except URLError:
                continue
            except Exception:
                continue
        return False, None, None
    finally:
        socket.setdefaulttimeout(original_timeout)


def main():
    configs = [
        ("http", 5),
        ("https", 5),
        ("socks5", 5),
        ("all", 10),
    ]

    grand = {"ok": 0, "fail": 0}
    for proto, count in configs:
        print(f"[{proto}] fetching {count}...")
        addrs = fetch(protocol=proto, count=count)
        if not addrs:
            print(f"[{proto}] no proxies\n")
            continue

        for addr in addrs:
            ok, lat, origin = test_proxy(addr)
            if ok:
                grand["ok"] += 1
                print(f"  ✅ {addr}  {lat*1000:.0f}ms  origin={origin}")
            else:
                grand["fail"] += 1
                print(f"  ❌ {addr}")
        print()

    total = grand["ok"] + grand["fail"]
    pct = grand["ok"] / total * 100 if total else 0
    print(f"=== {grand['ok']}/{total} passed ({pct:.0f}%) ===")
    print()


if __name__ == "__main__":
    main()