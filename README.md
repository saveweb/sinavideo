https://wiki.saveweb.org/sinavideo

## Setup instructions

1. Go to https://hq.saveweb.org/ to register.
2. Leave a message in our group so we can activate your account. (TG: https://t.me/saveweb_chat, IRC: stwp-chat:hackint.org, Matrix: #saveweb_chat:matrix.org)
3. Once your account is activated, create a machine token on the HQ.

4. (Optional but recommended) Use Watchtower to automatically update our containers:

```bash
sudo docker rm -f watchtower
sudo docker run -d \
  -v /var/run/docker.sock:/var/run/docker.sock  \
  -e 'WATCHTOWER_CLEANUP=true' \
  -e 'WATCHTOWER_POLL_INTERVAL=3600' \
  -e 'WATCHTOWER_INCLUDE_STOPPED=true' \
  -e 'WATCHTOWER_REVIVE_STOPPED=true' \
  -e 'WATCHTOWER_LABEL_ENABLE=true'   \
  --name watchtower --restart unless-stopped \
  dhm.saveweb.org/nickfedor/watchtower
```

5. Set `HQ_MACHINE_TOKEN` and start the container

```bash
export HQ_MACHINE_TOKEN=      # Set Your HQ_MACHINE_TOKEN HERE !!!!
```

```bash
if [[ -z "$HQ_MACHINE_TOKEN" ]]; then
    echo "WARN: HQ_MACHINE_TOKEN must be set"
    exit 1
fi

docker run -d \
  --name saveweb_archivesinavideo \
  --restart unless-stopped \
  --stop-timeout 120 \
  --log-driver json-file \
  --log-opt max-size=50m \
  --label=com.centurylinklabs.watchtower.enable=true \
  -e HQ_MACHINE_TOKEN="$HQ_MACHINE_TOKEN" \
  git.saveweb.org/saveweb/sinavideo:latest
```


## Notes

- Use clean internet connections.
- Do not use proxies or VPNs.
- Unless otherwise specified, please run only one archive instance per machine; do not run multiple instances.
- A single `HQ_MACHINE_TOKEN` can be used for multiple machines.
