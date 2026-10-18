# Test the unlock flow with Docker
Run from the repository root:

```sh
docker build -f tests/docker/Dockerfile -t azath-docker-e2e .
docker run --rm --network none azath-docker-e2e
```

The build downloads Go modules and base images. The test run has no external network access, uses disposable secrets, publishes no ports, and needs no host mounts. Containers are removed after the run; the local image remains.

## What runs
The suite starts the built `azath serve` as UID/GID 1000, Caddy with an internal CA, and a local Telegram API stub in one container. azath keeps its loopback-only listener. Real `azath seal` and `azath client` processes connect through Caddy using HTTPS URLs and trust the generated CA. A test executable stands in for `synoshare` and accepts only the expected disposable passphrase.

The Telegram stub enforces the 64-byte callback limit. Tests return captured button data through `getUpdates` to the real Telegram gate. They cover pending approval, wrong user/chat, denial, approval, cache hits, corrupt blobs, restart with persistent key/registry and cleared approvals, and disabled devices without prompts. Process and command waits have deadlines. Cleanup sends SIGTERM and bounds the drain before forcing a stop.

## Telegram API override
Use `azath serve --dev --telegram-api-base http://127.0.0.1:9000` to target a local Telegram API stub. The flag requires `--dev` and accepts an HTTP or HTTPS origin without credentials, paths, queries, or fragments. The default remains `https://api.telegram.org`.

Only use disposable bot tokens with a test endpoint. Development mode reads secret references from environment variables and skips memory locking. This suite does not validate the production 1Password resolver or memory-locking setup.

## Limits
This is an assembled-software test, not a DSM emulator. It does not validate actual shared-folder mounting, DSM Task Scheduler, DSM certificate storage, systemd sandboxing, live Telegram or 1Password, or two-server failover. Expired/tampered/replayed callback and blocked-send shutdown cases remain covered by focused Go tests rather than this Docker suite.

Keep manual recovery available. After this test passes, validate one disposable encrypted shared folder on the target NAS before enabling boot automation for existing folders.
