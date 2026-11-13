#!/usr/bin/env bash
# Starts the central VeryJudgyCat site on the organiser machine, and (optionally) a Cloudflare tunnel to it.
#   deploy/start-central.sh [--pool 2] [--addr 127.0.0.1:8080] [--tunnel NAME] [--no-tunnel] [--make-teams] [--dry-run]
# Secrets live in ~/.config/meow-judge (admin.pw, worker.token: generated once, mode 600).
# --make-teams creates the 86 team logins from teams-names.txt into teams.csv (refuses to overwrite).
# --tunnel NAME uses your named Cloudflare tunnel; otherwise a quick tunnel prints a random https://....trycloudflare.com address.
set -euo pipefail
cd "$(dirname "$0")/.."
CFG="$HOME/.config/meow-judge"; POOL=2; ADDR=127.0.0.1:8080; TUNNEL=""; NOTUNNEL=0; TEAMS=0; DRY=0
ROUNDS="1=mess,2=2-emergency"; DB=event.db   # a fresh database for the event (the old judge.db belongs to earlier testing)
while [ $# -gt 0 ]; do
  case "$1" in
    --pool) POOL="$2"; shift 2;;
    --addr) ADDR="$2"; shift 2;;
    --tunnel) TUNNEL="$2"; shift 2;;
    --no-tunnel) NOTUNNEL=1; shift;;
    --make-teams) TEAMS=1; shift;;
    --dry-run) DRY=1; shift;;
    *) echo "unknown option $1"; exit 2;;
  esac
done
run() { if [ "$DRY" = 1 ]; then echo "[dry-run] $*"; else "$@"; fi; }
# Avoid starting a second process and mistaking the existing server's health check for ours.
if [ "$DRY" != 1 ] && curl --noproxy '*' -fsS --max-time 2 "http://$ADDR/healthz" >/dev/null 2>&1; then
  echo "a site is already responding at http://$ADDR; leave it running or stop it before restarting"
  exit 1
fi
mkdir -p "$CFG"; chmod 700 "$CFG"
for f in admin.pw worker.token; do
  [ -s "$CFG/$f" ] || { ( umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=\n' > "$CFG/$f" ); echo "generated $CFG/$f"; }
done
[ -s "$CFG/grader.json" ] || { echo "missing $CFG/grader.json (the grader providers); see README"; exit 1; }
[ -x bin/judge ] || { echo "build first: CGO_ENABLED=0 go build -o bin/judge ./cmd/judge"; exit 1; }
for img in meow-judge-n8n:2.47.4 meow-judge-mock:2.47.4; do docker image inspect "$img" >/dev/null 2>&1 || { echo "missing image $img: run bin/judge bake"; exit 1; }; done
if [ "$TEAMS" = 1 ]; then
  [ -f "$CFG/teams-names.txt" ] || { echo "missing $CFG/teams-names.txt"; exit 1; }
  run bin/judge teams --db "$DB" --names "$CFG/teams-names.txt" --out teams.csv
  echo "logins are in teams.csv: print it, keep it private, never commit it"
fi
JUDGE_ADMIN_PASSWORD="$(cat "$CFG/admin.pw")"; JUDGE_WORKER_TOKEN="$(cat "$CFG/worker.token")"; JUDGE_LLM_CONFIG="$CFG/grader.json"
export JUDGE_ADMIN_PASSWORD JUDGE_WORKER_TOKEN JUDGE_LLM_CONFIG
echo "admin password: $CFG/admin.pw     worker token: $CFG/worker.token"
if [ "$DRY" = 1 ]; then echo "[dry-run] bin/judge serve --db $DB --rounds $ROUNDS --addr $ADDR --local-pool $POOL"; else
  mkdir -p logs
  nohup bin/judge serve --db "$DB" --rounds "$ROUNDS" --addr "$ADDR" --local-pool "$POOL" > logs/central.log 2>&1 &
  echo $! > logs/central.pid
  for _ in $(seq 1 30); do curl --noproxy '*' -sf "http://$ADDR/healthz" >/dev/null 2>&1 && break; sleep 1; done
  curl --noproxy '*' -sf "http://$ADDR/healthz" >/dev/null || { echo "the site did not start; see logs/central.log"; exit 1; }
  echo "site is up on http://$ADDR (log: logs/central.log, stop: kill \$(cat logs/central.pid))"
fi
SITE="http://$ADDR"
if [ "$NOTUNNEL" = 0 ]; then
  CF=$(command -v cloudflared || ls "/mnt/c/Program Files (x86)/cloudflared/cloudflared.exe" 2>/dev/null || true)
  [ -n "$CF" ] || { echo "cloudflared not found; run with --no-tunnel or install it"; exit 1; }
  if [ -n "$TUNNEL" ]; then
    run "$CF" tunnel run --url "http://$ADDR" "$TUNNEL" ; SITE="(your tunnel's https address)"
  elif [ "$DRY" = 1 ]; then echo "[dry-run] $CF tunnel --url http://$ADDR"; else
    nohup "$CF" tunnel --url "http://$ADDR" > logs/tunnel.log 2>&1 &
    echo $! > logs/tunnel.pid
    for _ in $(seq 1 40); do SITE=$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' logs/tunnel.log | head -1 || true); [ -n "$SITE" ] && break; sleep 1; done
    [ -n "$SITE" ] || { echo "no tunnel address yet; see logs/tunnel.log"; SITE="(see logs/tunnel.log)"; }
  fi
fi
echo
echo "Teams open:      $SITE"
echo "Organisers:      $SITE/admin/login   (password in $CFG/admin.pw)"
echo "Add a worker PC: sudo ./veryjudgycat.sh install --central $SITE --token \$(cat $CFG/worker.token) --pool 2"
