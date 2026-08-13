https://wiki.saveweb.org/sinavideo

## Setup instructions

1. Go to https://hq.saveweb.org/ to register.
2. Leave a message in our group so we can activate your account. (TG: https://t.me/saveweb_chat, IRC: stwp-chat:hackint.org, Matrix: #saveweb_chat:matrix.org)
3. Once your account is activated, create a machine token on the HQ.
4. Set your `HQ_MACHINE_TOKEN` in `compose.yml`.
5. Run `docker compose up -d`.

## Notes

- Use clean internet connections.
- Do not use proxies or VPNs.
- Unless otherwise specified, please run only one archive instance per machine; do not run multiple instances.
- A single `HQ_MACHINE_TOKEN` can be used for multiple machines.
