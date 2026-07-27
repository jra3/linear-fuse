#!/bin/sh
# nfpm scripts.postinstall for the .deb and .rpm (see .goreleaser.yaml).
# Runs as root at install time, so it only PRINTS guidance — the setup it
# describes lives under the installing user's $HOME, which root must not
# create here. Mirrors contrib/aur/linearfs-bin.install's post_install.
set -e

cat <<'EOF'
==> linearfs installed to /usr/bin/linearfs

    Finish setup (as your own user, not root):

    1. Provide your Linear API key (either works):
         export LINEAR_API_KEY=lin_api_xxxxx
       or
         mkdir -p ~/.config/linearfs
         printf 'api_key: lin_api_xxxxx\n' > ~/.config/linearfs/config.yaml
         chmod 600 ~/.config/linearfs/config.yaml

    2. Try it interactively:
         linearfs mount ~/linear
         linearfs status

    3. Or run it as a systemd *user* service (auto-mount on login). The unit
       reads ~/.config/linearfs/env, which must set at least LINEARFS_MOUNT:
         mkdir -p ~/.config/linearfs
         printf 'LINEARFS_MOUNT=%s/linear\nLINEAR_API_KEY=lin_api_xxxxx\n' "$HOME" \
             > ~/.config/linearfs/env
         chmod 600 ~/.config/linearfs/env
         systemctl --user enable --now linearfs.service

    Docs: /usr/share/doc/linearfs/   Security: keep the key file chmod 600.
EOF
