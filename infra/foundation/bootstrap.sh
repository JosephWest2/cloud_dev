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
# Ubuntu 24.04 removed the awscli deb; Canonical supports this classic snap.
# Credentials come from the instance profile via IMDSv2, never this script.
snap install aws-cli --classic
install -d -o root -g root -m 0755 /usr/local/libexec
install -d -o root -g root -m 0700 /etc/devbox
install -d -o root -g root -m 0700 /var/lib/devbox/execution
AWS_MAX_ATTEMPTS=3 AWS_RETRY_MODE=standard AWS_PAGER='' /snap/bin/aws s3api get-object \
  --region '@@DEVBOX_REGION@@' --expected-bucket-owner '@@DEVBOX_ACCOUNT@@' \
  --bucket '@@DEVBOX_RUNNER_BUCKET@@' \
  --key 'artifacts/runner/@@DEVBOX_RUNNER_SHA256@@/linux-amd64' \
  --cli-connect-timeout 10 --cli-read-timeout 60 \
  /var/lib/devbox/execution/runner.download >/dev/null
printf '%s  %s\n' '@@DEVBOX_RUNNER_SHA256@@' /var/lib/devbox/execution/runner.download | sha256sum --check --status
install -o root -g root -m 0755 /var/lib/devbox/execution/runner.download /usr/local/libexec/devbox-runner
rm /var/lib/devbox/execution/runner.download
cat > /etc/devbox/execution.json <<'DEVBOX_EXECUTION_CONFIG'
@@DEVBOX_WORKER_CONFIG@@
DEVBOX_EXECUTION_CONFIG
chmod 0600 /etc/devbox/execution.json
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
dpkg --compare-versions "$agent_version" ge 3.3.2746.0
systemctl enable --now snap.amazon-ssm-agent.amazon-ssm-agent.service
systemctl is-active --quiet snap.amazon-ssm-agent.amazon-ssm-agent.service
systemctl is-active --quiet ssh
install -o root -g root -m 0644 /dev/null /var/lib/devbox/bootstrap-complete
trap - ERR
