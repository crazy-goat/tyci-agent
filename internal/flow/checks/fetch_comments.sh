#!/usr/bin/env bash
# fetch_comments.sh: collects new PR comments from team members (state comments).
# Comment bodies are untrusted data: they only pass through jq, never through
# a shell command line. Authors need write or admin permission, else the comment
# is dropped (a failed permission lookup, such as 404, drops it too).
# A comment of the authenticated user is dropped only when it starts with the
# marker of post_review.sh, because tyci may run under a real person's account.
#
# Env in:  TYCI_PR, TYCI_REPO, TYCI_RUN_DIR, TYCI_LAST_COMMENT_ID (default 0).
# Keys:    none  nothing new for an agent
#          new   kept comments are in $TYCI_RUN_DIR/comments.md
#          fail  no PR, or a gh failure
# Writes the highest comment id seen (dropped ones too) to $TYCI_RUN_DIR/last_comment_id.
set -euo pipefail

pr="${TYCI_PR:-}"
repo="${TYCI_REPO:-}"
run="${TYCI_RUN_DIR:-}"
last="${TYCI_LAST_COMMENT_ID:-0}"
[ -n "$last" ] || last=0
case "$last" in *[!0-9]*) last=0 ;; esac
if [ -z "$pr" ] || [ -z "$run" ]; then
    echo "fetch_comments.sh: TYCI_PR or TYCI_RUN_DIR is not set" >&2
    echo fail
    exit 0
fi
marker='<!-- tyci-agent -->'
rm -f "$run/comments.md"

fail() {
    echo "fetch_comments.sh: $1" >&2
    echo fail
    exit 0
}

list() {
    gh api --paginate "repos/$repo/$1/$pr/comments" | jq -s '[.[][]]'
}

pulls=$(list pulls) || fail "gh failed for pull request comments"
issue=$(list issues) || fail "gh failed for issue comments"
own=$(gh api user -q .login) || fail "gh failed for the own login"

all=$(jq -n --argjson a "$pulls" --argjson b "$issue" --argjson last "$last" \
    '$a + $b | map({id, user: (.user.login // ""), url: .html_url, body: (.body // "")}) | map(select(.id > $last))') ||
    fail "bad comment JSON"

max=$(jq -r --argjson last "$last" '[.[].id] | max // $last' <<<"$all")
printf '%s\n' "$max" >"$run/last_comment_id"

allowed='[]'
while IFS= read -r user; do
    [[ "$user" =~ ^[A-Za-z0-9-]+$ ]] || continue
    perm=$(gh api "repos/$repo/collaborators/$user/permission" --jq .permission 2>/dev/null) || continue
    if [ "$perm" = write ] || [ "$perm" = admin ]; then
        allowed=$(jq -c --arg u "$user" '. + [$u]' <<<"$allowed")
    fi
done < <(jq -r '[.[].user] | unique | .[]' <<<"$all")

kept=$(jq --argjson allowed "$allowed" --arg own "$own" --arg marker "$marker" \
    'map(select(.user as $u | $allowed | index($u))
         | select((.user == $own and (.body | startswith($marker))) | not))' <<<"$all")

if [ "$(jq length <<<"$kept")" -eq 0 ]; then
    echo none
    exit 0
fi
jq -r 'to_entries[] | "## Comment \(.key + 1) by \(.value.user) (\(.value.url))\n\(.value.body)\n"' <<<"$kept" >"$run/comments.md"
echo new
