#!/usr/bin/env bash
# VeryJudgyCat worker PC script: one file for everything.
#   sudo ./veryjudgycat.sh install   [--central URL] [--token TOKEN] [--pool 2] [--skip-docker] [--dry-run]
#   sudo ./veryjudgycat.sh status
#   sudo ./veryjudgycat.sh uninstall [--keep-docker] [--dry-run]
# Run it from the copied worker-bundle folder (it needs `judge` and `images.tar.gz` beside it).
# install: installs Docker from Ubuntu's repo if missing (and remembers that), loads the judge images, saves the
#          token in a root-only file, starts a service, and checks the connection to the central site.
# uninstall: removes the service, containers, network, images, /opt/veryjudgycat and Docker (only if we installed it).
set -uo pipefail
APP=/opt/veryjudgycat
UNIT=/etc/systemd/system/veryjudgycat-worker.service
HERE="$(cd "$(dirname "$0")" && pwd)"
CMD="${1:-help}"; [ $# -gt 0 ] && shift
CENTRAL=""; TOKEN=""; POOL=2; DRY=0; SKIPDOCKER=0; KEEP=0
while [ $# -gt 0 ]; do
  case "$1" in
    --central) CENTRAL="${2%/}"; shift 2;;
    --token) TOKEN="$2"; shift 2;;
    --pool) POOL="$2"; shift 2;;
    --skip-docker) SKIPDOCKER=1; shift;;   # Docker is already there (any distro)
    --keep-docker) KEEP=1; shift;;
    --dry-run) DRY=1; shift;;
    *) echo "unknown option $1"; exit 2;;
  esac
done
say() { printf '\n== %s\n' "$*"; }
run() { if [ "$DRY" = 1 ]; then echo "[dry-run] $*"; else eval "$@"; fi; }   # eval on purpose: some steps are pipelines
quiet() { if [ "$DRY" = 1 ]; then echo "[dry-run] $*"; else eval "$@" >/dev/null 2>&1; fi; }
need_root() { [ "$DRY" = 1 ] || [ "$(id -u)" = 0 ] || { echo "run with sudo:  sudo $0 $CMD ..."; exit 1; }; }

ping_central() { # 0 = reachable and token accepted
  local code
  code=$(curl -s -o /dev/null -m 10 -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$CENTRAL/api/worker/ping" 2>/dev/null)
  case "$code" in
    200) echo "connected to $CENTRAL, token accepted"; return 0;;
    401) echo "reached $CENTRAL but the token was refused: check the worker token"; return 1;;
    000) echo "cannot reach $CENTRAL (no network, wrong address, or the site is down)"; return 1;;
    *) echo "unexpected answer HTTP $code from $CENTRAL"; return 1;;
  esac
}

do_install() {
  need_root
  if [ -z "$CENTRAL" ] && [ -t 0 ]; then read -r -p "Central site URL (https://...): " CENTRAL; CENTRAL="${CENTRAL%/}"; fi
  if [ -z "$TOKEN" ] && [ -t 0 ]; then read -r -s -p "Worker token: " TOKEN; echo; fi
  [ -n "$CENTRAL" ] && [ -n "$TOKEN" ] || { echo "need --central and --token"; exit 2; }
  for f in judge images.tar.gz; do [ -f "$HERE/$f" ] || { echo "missing $HERE/$f: copy the whole worker-bundle folder"; exit 1; }; done
  [ "$DRY" = 1 ] || { ( cd "$HERE" && sha256sum -c SHA256SUMS >/dev/null ) || { echo "the files are damaged; copy the bundle again"; exit 1; }; }

  say "Checking the connection first"
  if [ "$DRY" = 1 ]; then echo "[dry-run] ping $CENTRAL/api/worker/ping"; else
    ping_central || { echo "fix that and run again (nothing has been installed yet)"; exit 1; }
  fi

  mem_mb=$(awk '/MemTotal/ {print int($2/1024)}' /proc/meminfo)
  echo "RAM ${mem_mb} MB, pool ${POOL} (about 1 GB per slot while testing)"
  [ "$mem_mb" -ge $((POOL*1100+1500)) ] || echo "warning: tight for pool ${POOL}; consider --pool 1"

  run "mkdir -p $APP"
  if ! command -v docker >/dev/null 2>&1 && [ "$SKIPDOCKER" = 0 ]; then
    say "Installing Docker (docker.io from the Ubuntu repository)"
    command -v apt-get >/dev/null 2>&1 || [ "$DRY" = 1 ] || { echo "no apt-get: install Docker yourself, then use --skip-docker"; exit 1; }
    run "DEBIAN_FRONTEND=noninteractive apt-get update -qq >/var/log/veryjudgycat-apt.log 2>&1" || { echo "apt-get update failed (no internet?); see /var/log/veryjudgycat-apt.log"; exit 1; }
    run "DEBIAN_FRONTEND=noninteractive apt-get install -y -qq docker.io >>/var/log/veryjudgycat-apt.log 2>&1" || { echo "Docker install failed; see /var/log/veryjudgycat-apt.log"; exit 1; }
    run "touch $APP/.docker-installed-by-us"   # so uninstall may remove it later
  fi
  run "systemctl enable --now docker" || { echo "could not start Docker"; exit 1; }
  [ "$DRY" = 1 ] || for _ in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done

  say "Loading the judge images"
  run "install -m 0755 '$HERE/judge' $APP/judge"
  run "gunzip -c '$HERE/images.tar.gz' | docker load" || { echo "loading the images failed"; exit 1; }

  say "Starting the worker service"
  if [ "$DRY" = 1 ]; then echo "[dry-run] write $APP/worker.env (mode 600) and $UNIT"; else
    ( umask 077; printf 'JUDGE_CENTRAL=%s\nJUDGE_WORKER_TOKEN=%s\n' "$CENTRAL" "$TOKEN" > $APP/worker.env )
    cat > $UNIT <<EOF
[Unit]
Description=VeryJudgyCat test worker
After=docker.service network-online.target
Requires=docker.service

[Service]
EnvironmentFile=$APP/worker.env
ExecStart=$APP/judge worker --pool $POOL
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
  fi
  run "systemctl daemon-reload"
  run "systemctl enable veryjudgycat-worker" || { echo "could not enable the service"; exit 1; }
  run "systemctl restart veryjudgycat-worker" || { echo "could not start the service"; exit 1; }
  if [ "$DRY" != 1 ]; then
    sleep 8
    if systemctl is-active --quiet veryjudgycat-worker; then echo "worker is running"; else echo "worker did not stay up:"; journalctl -u veryjudgycat-worker -n 15 --no-pager; exit 1; fi
  fi
  if [ "$DRY" = 1 ]; then say "Dry run finished: nothing was changed"; return; fi
  say "Installed"
  echo "Check it any time:  sudo $0 status      Remove everything after the event:  sudo $0 uninstall"
}

do_status() {
  need_root
  [ -f "$APP/worker.env" ] && { set -a; . "$APP/worker.env"; set +a; CENTRAL="${JUDGE_CENTRAL:-}"; TOKEN="${JUDGE_WORKER_TOKEN:-}"; }
  echo "docker:  $(command -v docker >/dev/null 2>&1 && (docker info >/dev/null 2>&1 && echo running || echo installed-but-not-running) || echo missing)"
  echo "images:  $(docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | grep -c '^meow-judge' || true) of 2 judge images loaded"
  echo "service: $(systemctl is-active veryjudgycat-worker 2>/dev/null || echo not-installed)"
  echo "judge containers: $(docker ps -q --filter label=meow.judge 2>/dev/null | wc -l)"
  if [ -n "$CENTRAL" ]; then ping_central; else echo "central: not configured"; fi
  echo "--- last log lines"; journalctl -u veryjudgycat-worker -n 8 --no-pager 2>/dev/null || true
}

do_uninstall() {
  need_root
  say "Stopping the worker"
  quiet "systemctl disable --now veryjudgycat-worker"
  quiet "rm -f $UNIT"
  quiet "systemctl daemon-reload"
  if command -v docker >/dev/null 2>&1; then
    say "Removing judge containers, network and images"
    quiet "docker ps -aq --filter label=meow.judge | xargs -r docker rm -f"
    quiet "docker rm -f meow-judge-mock"
    quiet "docker network rm meow-judge-net"
    quiet "docker rmi -f meow-judge-n8n:2.47.4 meow-judge-mock:2.47.4"
  fi
  MARK=0; [ -f "$APP/.docker-installed-by-us" ] && MARK=1
  run "rm -rf $APP"   # binary, token file, marker
  if [ "$MARK" = 1 ] && [ "$KEEP" = 0 ]; then
    say "Removing Docker (this script installed it)"
    quiet "systemctl disable --now docker docker.socket containerd"
    quiet "DEBIAN_FRONTEND=noninteractive apt-get purge -y -qq docker.io containerd runc"
    quiet "DEBIAN_FRONTEND=noninteractive apt-get autoremove --purge -y -qq"
    quiet "rm -rf /var/lib/docker /var/lib/containerd /var/log/veryjudgycat-apt.log"
    quiet "rmdir /opt/containerd"                                   # empty dir left by the containerd package
    quiet "getent group docker >/dev/null && groupdel docker"
  elif [ "$MARK" = 1 ]; then echo "kept Docker as asked"; else echo "Docker was already on this PC, so it was left alone"; fi
  say "Done"
  echo "Delete the copied worker-bundle folder too."
}

case "$CMD" in
  install) do_install;;
  status) do_status;;
  uninstall) do_uninstall;;
  *) sed -n '2,10p' "$0"; exit 2;;
esac
