#!/bin/sh
# Push the beeircd.org site to the VPS.
#   scripts/site-push.sh              index.html, art/, progress.json, whatsnew.json
#   scripts/site-push.sh progress     only progress.json
#   scripts/site-push.sh docs [ref]   rebuild beeircd.org/docs from the newest release tag (or ref) and swap it in
#   scripts/site-push.sh release vX   after publishing release vX: What's new, change log and,
#                                     for a release (not an rc), the docs with that version's HELP texts
set -e
cd "$(dirname "$0")/../site"
REPO=../.wt/stage3

push_home() {
  for f in $1; do [ "${f%.json}" = "$f" ] || python -c "import json,sys; json.load(open(sys.argv[1], encoding='utf-8'))" "$f"; done
  scp -q $1 vps:/tmp/
  ssh vps "cd /tmp && install -m 644 -o root -g root $1 /var/www/beeircd.org/ && rm $1"
  curl -fsS https://beeircd.org/progress.json >/dev/null && echo "beeircd.org updated: $1"
}

# push_art: hero pictures in site/art/ -> beeircd.org/art/
push_art() {
  [ -d art ] || return 0
  tar czf /tmp/beeircd-art.tgz art
  scp -q /tmp/beeircd-art.tgz vps:/tmp/ && rm /tmp/beeircd-art.tgz
  ssh vps "cd /var/www/beeircd.org && tar xzf /tmp/beeircd-art.tgz --no-same-owner && chown -R root:root art && chmod 755 art && chmod 644 art/* && rm /tmp/beeircd-art.tgz"
  echo "beeircd.org updated: art/"
}

# build_docs <tag> [--current=X.Y]: build from a clean checkout of the tag, check, push
build_docs() {
  git -C "$REPO" fetch -q --tags origin || true
  wt=$(mktemp -d)
  git -C "$REPO" worktree add -q --detach "$wt/src" "$1"
  trap 'git -C "$REPO" worktree remove --force "$wt/src" 2>/dev/null; rm -rf "$wt"' EXIT
  [ -n "$CAPTURE" ] && python docs-gen/capture_help.py "$wt/src" "$CAPTURE"
  python docs-gen/build.py "$wt/src" $2
  python docs-gen/check.py | grep -v '^EXT' | tail -1 | grep -q ', 0 bad links' || { echo "broken links, docs not pushed"; exit 1; }
  rm -f docs/_urls.json
  tar -czf "$wt/docs.tgz" -C docs .
  scp -q "$wt/docs.tgz" vps:/tmp/beeircd-docs.tgz
  ssh vps 'set -e; d=/var/www/beeircd.org; rm -rf $d/docs.new; mkdir $d/docs.new;
    tar -xzf /tmp/beeircd-docs.tgz -C $d/docs.new; chown -R root:root $d/docs.new;
    find $d/docs.new -type d -exec chmod 755 {} +; find $d/docs.new -type f -exec chmod 644 {} +;
    rm -rf $d/docs.old; [ -d $d/docs ] && mv $d/docs $d/docs.old; mv $d/docs.new $d/docs; rm -rf $d/docs.old /tmp/beeircd-docs.tgz'
  curl -fsS https://beeircd.org/docs/ >/dev/null && echo "beeircd.org/docs updated from $1"
}

latest_release() {
  git -C "$REPO" fetch -q --tags origin || true
  git -C "$REPO" tag --list 'v*' | grep -v -- - | sort -V | tail -1
}

case "$1" in
  progress)
    push_home progress.json ;;
  docs)
    ref=${2:-$(latest_release)}
    cur=$(cat docs-gen/data/current.txt)
    if [ -z "$2" ] && ! echo "$ref" | grep -q "^v$cur\."; then
      echo "the newest release $ref is not $cur; give the ref to build from: site-push.sh docs <ref>"; exit 1
    fi
    build_docs "$ref" ;;
  release)
    tag=$2
    [ -n "$tag" ] || { echo "usage: site-push.sh release vX.Y.Z"; exit 1; }
    python docs-gen/whatsnew.py
    case "$tag" in
      *-*) echo "$tag is a test version: What's new shows it as Testing now; docs stay on $(latest_release)" ;;
      *) ver=$(echo "$tag" | sed 's/^v\([0-9]*\.[0-9]*\).*/\1/')
         CAPTURE=$ver build_docs "$tag" "--current=$ver" ;;
    esac
    push_home "index.html whatsnew.json" ;;
  *)
    push_art
    push_home "index.html progress.json whatsnew.json" ;;
esac
