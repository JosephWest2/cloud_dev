#!/bin/bash
# Only the validated public key is rendered here; no private keys or credentials.
set -euo pipefail
umask 022
install -d -o root -g root -m 0755 /var/lib/devbox
rm -f /var/lib/devbox/bootstrap-complete /var/lib/devbox/bootstrap-failed
trap 'touch /var/lib/devbox/bootstrap-failed' ERR
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq openssh-server
id devbox >/dev/null 2>&1 || useradd --create-home --shell /bin/bash devbox
# An unusable password without locking public-key authentication.
usermod --password '*' devbox
install -d -o devbox -g devbox -m 0700 /home/devbox/.ssh
printf '%s\n' '@@DEVBOX_PUBLIC_KEY@@' > /home/devbox/.ssh/authorized_keys
chown devbox:devbox /home/devbox/.ssh/authorized_keys
chmod 0600 /home/devbox/.ssh/authorized_keys
cat > /etc/ssh/sshd_config.d/00-devbox.conf <<'SSH'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
PubkeyAuthentication yes
AllowUsers devbox
SSH
# Development account may administer its own disposable machine.
printf '%s\n' 'devbox ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/devbox
chmod 0440 /etc/sudoers.d/devbox
visudo -cf /etc/sudoers.d/devbox
ssh-keygen -A
# Noble uses socket activation; ssh.service may not have created this yet.
install -d -o root -g root -m 0755 /run/sshd
/usr/sbin/sshd -t
systemctl enable --now ssh
systemctl restart ssh
# Canonical's selected standard server image includes the SSM agent snap.
agent_version=$(snap list amazon-ssm-agent | awk 'NR == 2 {print $2}')
dpkg --compare-versions "$agent_version" ge 3.3.40.0
systemctl enable --now snap.amazon-ssm-agent.amazon-ssm-agent.service
systemctl is-active --quiet snap.amazon-ssm-agent.amazon-ssm-agent.service
systemctl is-active --quiet ssh
install -o root -g root -m 0644 /dev/null /var/lib/devbox/bootstrap-complete
trap - ERR
