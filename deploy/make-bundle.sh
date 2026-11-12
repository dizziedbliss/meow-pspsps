#!/usr/bin/env bash
# Run ON THE ORGANISER MACHINE (needs Docker + the baked images): builds ./dist/worker-bundle/, a folder
# you copy to each worker PC (USB stick, shared drive, or `python3 -m http.server`).
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=dist/worker-bundle
rm -rf "$OUT"; mkdir -p "$OUT"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$OUT/judge" ./cmd/judge
for img in meow-judge-n8n:2.47.4 meow-judge-mock:2.47.4; do
  docker image inspect "$img" >/dev/null || { echo "missing image $img: run bin/judge bake first"; exit 1; }
done
echo "saving images (about 1 GB compressed, takes a minute)..."
docker save meow-judge-n8n:2.47.4 meow-judge-mock:2.47.4 | gzip -1 > "$OUT/images.tar.gz"
cp deploy/veryjudgycat.sh "$OUT/"
chmod +x "$OUT"/*.sh "$OUT/judge"
( cd "$OUT" && sha256sum judge images.tar.gz > SHA256SUMS )
du -sh "$OUT"
echo "done: copy $OUT to each worker PC, then there: sudo ./veryjudgycat.sh install --central https://<site> --token <worker token> --pool 2"
