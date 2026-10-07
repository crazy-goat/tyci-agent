#!/usr/bin/env bash
# fetch_comments.sh: collects new PR comments from team members (state comments).
# Comment bodies are untrusted data: they only pass through jq, never through
# a shell command line. Authors need write or admin permission, else the comment
# is dropped (a 404 on the permission lookup drops it; any other lookup failure gives key fail).
# A comment of the authenticated user is dropped only when it starts with the
# marker of post_review.sh, because tyci may run under a real person's account.
#
# Env in:  TYCI_PR, TYCI_REPO, TYCI_RUN_DIR, TYCI_ARTIFACT_DIR, TYCI_LAST_COMMENT_ID,
#          TYCI_LAST_REVIEW_COMMENT_ID (default 0).
# Keys:    none  nothing new for an agent
#          new   kept comments are in $TYCI_ARTIFACT_DIR/comments.md
#          fail  no PR, or a gh failure
# Writes the highest ids seen (dropped ones too) to $TYCI_RUN_DIR/last_comment_id (issue comments)
# and last_review_comment_id (PR review comments), only when no lookup failed.
set -euo pipefail

pr="${TYCI_PR:-}"
repo="${TYCI_REPO:-}"
run="${TYCI_RUN_DIR:-}"
art="${TYCI_ARTIFACT_DIR:-}"
last="${TYCI_LAST_COMMENT_ID:-0}"
last_review="${TYCI_LAST_REVIEW_COMMENT_ID:-0}"
case "$last" in '' | *[!0-9]*) last=0 ;; esac
case "$last_review" in '' | *[!0-9]*) last_review=0 ;; esac
if [ -z "$pr" ] || [ -z "$run" ] || [ -z "$art" ]; then
    echo "fetch_comments.sh: TYCI_PR, TYCI_RUN_DIR or TYCI_ARTIFACT_DIR is not set" >&2
    echo fail
    exit 0
fi
marker='<!-- tyci-agent -->'

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

# Review comments and issue comments have separate id sequences: one mark each.
# shellcheck disable=SC2016 # jq program, not a shell expansion
norm='map({id, user: (.user.login // ""), url: .html_url, body: (.body // "")}) | map(select(.id > $last))'
pulls=$(jq --argjson last "$last_review" "$norm" <<<"$pulls") || fail "bad comment JSON"
issue=$(jq --argjson last "$last" "$norm" <<<"$issue") || fail "bad comment JSON"
all=$(jq -n --argjson a "$pulls" --argjson b "$issue" '$a + $b')
max_review=$(jq -r --argjson last "$last_review" '[.[].id] | max // $last' <<<"$pulls")
max_issue=$(jq -r --argjson last "$last" '[.[].id] | max // $last' <<<"$issue")

allowed='[]'
while IFS= read -r user; do
    [[ "$user" =~ ^[A-Za-z0-9-]+$ ]] || continue
    if perm=$(gh api "repos/$repo/collaborators/$user/permission" --jq .permission 2>&1); then
        if [ "$perm" = write ] || [ "$perm" = admin ]; then
            allowed=$(jq -c --arg u "$user" '. + [$u]' <<<"$allowed")
        fi
    elif [[ "$perm" == *404* ]]; then
        continue
    else
        fail "permission lookup failed for $user"
    fi
done < <(jq -r '[.[].user] | unique | .[]' <<<"$all")

# Advance the marks only after every lookup worked.
printf '%s\n' "$max_issue" >"$run/last_comment_id"
printf '%s\n' "$max_review" >"$run/last_review_comment_id"

kept=$(jq --argjson allowed "$allowed" --arg own "$own" --arg marker "$marker" \
    'map(select(.user as $u | $allowed | index($u))
         | select((.user == $own and (.body | startswith($marker))) | not))' <<<"$all")

if [ "$(jq length <<<"$kept")" -eq 0 ]; then
    echo none
    exit 0
fi
jq -r 'to_entries[] | "## Comment \(.key + 1) by \(.value.user) (\(.value.url))\n\(.value.body)\n"' <<<"$kept" >"$art/comments.md"
echo new
