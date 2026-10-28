#!/usr/bin/env bash
# release_tag.sh: creates the annotated release tag on the merge commit of the release PR (state tag, #207).
# The tag name is the milestone title (vX.Y.Z). The script creates and pushes a tag, and nothing else:
# it never moves and never deletes a tag. A tag that exists locally or on origin is refused.
# The script refuses without a human answer "yes" in the state ask_release: a line of approvals.txt
# with state "ask_release" and answer "yes" (written by the runner, see Runner.recordApproval).
#
# Env in:  TYCI_PR, TYCI_REPO, TYCI_DEFAULT_BRANCH, TYCI_RUN_DIR, TYCI_PARAM_MILESTONE.
#          cwd = the run worktree.
# Keys:    tagged  the tag was created on the merge commit and pushed to origin
#          fail    no approval, the tag exists, the PR is not merged, or a git or gh call failed
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi
# lib_forge.sh selects gh or `tyci forge` (GitLab). A copy of this script without it works only for GitHub.
forge_lib="$(dirname "${BASH_SOURCE[0]}")/lib_forge.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$forge_lib" ]; then . "$forge_lib"; else is_gitlab() { [ "${TYCI_FORGE:-github}" != gitlab ] && return 1; echo "$(basename "$0"): lib_forge.sh is missing" >&2; exit 1; }; github_only() { is_gitlab || :; }; fi
github_only fail

tag="${TYCI_PARAM_MILESTONE:-}"
if [ -z "$tag" ] || [ -z "${TYCI_PR:-}" ] || [ -z "${TYCI_REPO:-}" ] ||
    [ -z "${TYCI_DEFAULT_BRANCH:-}" ] || [ -z "${TYCI_RUN_DIR:-}" ]; then
    describe fail "the milestone param, TYCI_PR, TYCI_REPO, TYCI_DEFAULT_BRANCH or TYCI_RUN_DIR is not set" \
        "the run was started without its milestone or the PR was not opened" \
        "return failed; the release run cannot tag without them"
    echo fail
    exit 0
fi

# stop <what> <likely cause> <suggested>: prints the failure block and the key, then ends the script.
stop() {
    describe fail "$@"
    echo fail
    exit 0
}

if [ ! -f "$TYCI_RUN_DIR/approvals.txt" ] ||
    ! awk -F'\t' '$2 == "ask_release" && $3 == "yes" { found = 1 } END { exit !found }' "$TYCI_RUN_DIR/approvals.txt"; then
    stop "no human approval for the release of $tag: approvals.txt has no ask_release answer yes; nothing was tagged" \
        "the run reached the tag state without the answer yes to the release question" \
        "return failed; a human starts the release again and answers yes"
fi

remote=$(git ls-remote --tags origin "refs/tags/$tag") ||
    stop "git ls-remote --tags origin failed (see the git output above); nothing was tagged" \
        "no network or git is not authenticated" \
        "fix the connection to origin, then start the release again"
if [ -n "$remote" ]; then
    stop "tag $tag already exists on origin; the script never moves or deletes a tag" \
        "an earlier release or a person created the tag" \
        "return failed; a human checks the tag with 'git ls-remote --tags origin' and decides"
fi
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
    stop "tag $tag already exists in the local repository; the script never moves or deletes a tag" \
        "an earlier run created the tag but did not push it" \
        "return failed; a human checks the tag with 'git show $tag' and pushes it or deletes it by hand"
fi

sha=$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json mergeCommit --jq '.mergeCommit.oid // ""') ||
    stop "gh pr view $TYCI_PR failed (see the gh output above); nothing was tagged" \
        "no network, gh is not logged in, or a temporary GitHub problem" \
        "run 'gh auth status'; return failed and start the release again when gh works"
if [ -z "$sha" ]; then
    stop "PR #$TYCI_PR has no merge commit, so there is nothing to tag" \
        "the PR was not merged" \
        "return failed; merge the PR first, then start the release again"
fi

git fetch -q origin "$TYCI_DEFAULT_BRANCH" ||
    stop "git fetch origin $TYCI_DEFAULT_BRANCH failed (see the git output above); nothing was tagged" \
        "no network or git is not authenticated" \
        "fix the connection to origin, then start the release again"
git merge-base --is-ancestor "$sha" "origin/$TYCI_DEFAULT_BRANCH" ||
    stop "merge commit $sha of PR #$TYCI_PR is not in origin/$TYCI_DEFAULT_BRANCH; nothing was tagged" \
        "the merge commit is not on the default branch" \
        "return failed; check PR #$TYCI_PR with 'gh pr view $TYCI_PR'"

git tag -a "$tag" -m "Release $tag" "$sha" ||
    stop "git tag $tag failed (see the git output above); nothing was pushed" \
        "git has no committer identity or the tag name is not valid" \
        "check 'git config user.name' and 'git config user.email'; return failed and start the release again"

if ! git push origin "refs/tags/$tag:refs/tags/$tag" >&2; then
    stop "git push of tag $tag failed (see the git output above). The local tag stays; it was not moved or deleted" \
        "no network, no push permission, or a tag protection rule" \
        "run 'git push origin refs/tags/$tag:refs/tags/$tag' by hand after the cause is fixed; return failed"
fi

echo tagged
