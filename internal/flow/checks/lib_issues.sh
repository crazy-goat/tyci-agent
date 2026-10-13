#!/usr/bin/env bash
# lib_issues.sh: sourced by findings_collect.sh and findings_apply.sh (it is not a state of its own).
# Issue helpers of the milestone-close workflow (#206). Issue text is data: the helpers only pass it
# through jq, never through a shell command line.
#
# issues_open_nomilestone  prints one JSON object per line for each open issue without a milestone.
#                          Pull requests are skipped, every page is read. When TYCI_EXCLUDE_LABEL is
#                          set, issues with that label are skipped too.
# author_may_write <login> exit 0 when the login has write or admin permission on TYCI_REPO,
#                          exit 1 when it has not (or is not a GitHub login), exit 2 on a gh failure.
#                          The login rules are the ones of issue_done.sh.
# Env in:  TYCI_REPO (owner/name), optional TYCI_EXCLUDE_LABEL.

issues_open_nomilestone() {
    gh api --paginate "repos/$TYCI_REPO/issues?state=open&milestone=none&per_page=100" |
        jq -c --arg l "${TYCI_EXCLUDE_LABEL:-}" '.[] | select(has("pull_request") | not)
            | select($l == "" or ([.labels[].name] | index($l) == null))'
}

author_may_write() {
    local login="${1#app/}" perm
    if ! [[ "$login" =~ ^[A-Za-z0-9][A-Za-z0-9-]*(\[bot\])?$ ]]; then
        return 1
    fi
    if perm=$(gh api "repos/$TYCI_REPO/collaborators/$login/permission" --jq .permission 2>&1); then
        case "$perm" in
        write | admin) return 0 ;;
        *) return 1 ;;
        esac
    fi
    if [[ "$perm" == *404* ]]; then
        return 1
    fi
    return 2
}
