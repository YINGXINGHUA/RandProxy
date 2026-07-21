#!/usr/bin/env python3
"""
Probe v2: fetch proxies, test HTTP + HTTPS (CONNECT) forwarding.
Target service: https://api.literouter.com (HTTPS)
Zero dependencies.
"""

import json
import socket
import time
import urllib.request
from urllib.error import URLError

API_URL = "https://proxy.scdn.io/api/get_proxy.php"
TIMEOUT = 5  # per proxy, per scheme

TEST_HTTP = "http://httpbin.org/ip"
TEST_HTTPS = "https://httpbin.org/ip"


def fetch(protocol="all", count=5):
    url = f"{API_URL}?protocol={protocol}&count={count}"
    try:
        resp = urllib.request.urlopen(url, timeout=10)
        data = json.loads(resp.read())
        if data.get("code") == 200:
            return data["data"]["proxies"]
        print(f"  [api] {data.get('message')}")
    except Exception as e:
        print(f"  [api] {e}")
    return []


def test_proxy(addr):
    """Test HTTP + HTTPS forwarding. Return dict of results."""
    result = {"addr": addr, "http": None, "https": None}

    def _try_scheme(scheme, test_url):
        hdl = urllib.request.ProxyHandler({"http": addr, "https": addr})
        opener = urllib.request.build_opener(hdl)
        original_timeout = socket.getdefaulttimeout()
        socket.setdefaulttimeout(TIMEOUT)
        try:
            t0 = time.time()
            resp = opener.open(test_url, timeout=TIMEOUT)
            elapsed = time.time() - t0
            body = resp.read().decode()
            origin = json.loads(body).get("origin", "?")
            return {"ok": True, "ms": round(elapsed * 1000), "origin": origin}
        except Exception:
            return {"ok": False}
        finally:
            socket.setdefaulttimeout(original_timeout)

    result["http"] = _try_scheme("http", TEST_HTTP)

    if result["http"]["ok"]:
        # only test HTTPS if HTTP already works
        result["https"] = _try_scheme("https", TEST_HTTPS)
    else:
        result["https"] = {"ok": False, "skip": True}

    return result


def main():
    configs = [("http", 10), ("https", 10), ("all", 10)]

    grand = {"pass": 0, "partial": 0, "fail": 0}

    for proto, count in configs:
        print(f"\n--- protocol={proto} x{count} ---")
        addrs = fetch(protocol=proto, count=count)
        if not addrs:
            continue

        for addr in addrs:
            r = test_proxy(addr)
            h, hs = r["http"], r["https"]

            if h["ok"] and hs["ok"]:
                grand["pass"] += 1
                status = f"✅ HTTP+HTTPS  {h['ms']}ms/{hs['ms']}ms  origin={h['origin']}"
            elif h["ok"] and not hs["ok"]:
                grand["partial"] += 1
                status = f"⚠️  HTTP only   {h['ms']}ms  (HTTPS failed)"
            else:
                grand["fail"] += 1
                status = f"❌ dead"

            print(f"  {status}  {addr}")

    total = sum(grand.values())
    print(f"\n=== Summary ===")
    print(f"  ✅ HTTP+HTTPS: {grand['pass']}")
    print(f"  ⚠️  HTTP only:  {grand['partial']}")
    print(f"  ❌ Dead:       {grand['fail']}")
    print(f"  {'─' * 30}")
    print(f"  HTTPS usable: {grand['pass']}/{total} ({grand['pass']/total*100:.0f}%)")


if __name__ == "__main__":
    main()