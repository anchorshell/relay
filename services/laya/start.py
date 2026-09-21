"""Explicit loopback-only entry point. Remote deployments use an authenticated TLS proxy."""
import os

if __name__ == "__main__":
    import uvicorn
    # Access logs would reveal client information; the API never logs text.
    uvicorn.run("worker:app", host="127.0.0.1", port=int(os.environ.get("LAYA_PORT", "11731")),
                workers=1, access_log=False, limit_concurrency=16,
                timeout_keep_alive=5, timeout_graceful_shutdown=35)
