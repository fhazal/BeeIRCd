#!/bin/sh
# Push the beeircd.org site to the VPS. Usage: scripts/site-push.sh [progress]
#   no argument: index.html + progress.json; "progress": only progress.json.
set -e
cd "$(dirname "$0")/../site"
python -c "import json; json.load(open('progress.json', encoding='utf-8'))"
files="index.html progress.json"
[ "$1" = progress ] && files=progress.json
scp -q $files vps:/tmp/
ssh vps "cd /tmp && install -m 644 -o root -g root $files /var/www/beeircd.org/ && rm $files"
curl -fsS https://beeircd.org/progress.json >/dev/null && echo "beeircd.org updated: $files"
