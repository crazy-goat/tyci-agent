#!/usr/bin/env bash
# lib_redact.sh: sourced by ci_failure.sh, verify.sh and bump_deps.sh (it is not a state of its own).
# redact reads stdin and writes it to stdout with the common secret patterns replaced by <redacted>.
# A log that an agent reads must pass through it. The list is basic: it does not replace the
# secret scanning of GitHub. It covers GitHub tokens (ghp_, github_pat_), OpenAI-style keys (sk-),
# AWS access key ids (AKIA), Authorization headers, NAME=value pairs whose name holds TOKEN, SECRET,
# PASSWORD or API_KEY, and private key blocks. It needs only sed (bash 3.2 and BSD sed compatible).

redact() {
    sed -E \
        -e 's/ghp_[A-Za-z0-9]{20,}/<redacted>/g' \
        -e 's/github_pat_[A-Za-z0-9_]{20,}/<redacted>/g' \
        -e 's/sk-[A-Za-z0-9_-]{20,}/<redacted>/g' \
        -e 's/AKIA[0-9A-Z]{16}/<redacted>/g' \
        -e 's/([Aa]uthorization:[[:space:]]*[Bb]earer[[:space:]]+)[^[:space:]]+/\1<redacted>/g' \
        -e 's/([A-Za-z0-9_]*(TOKEN|SECRET|PASSWORD|API_KEY)[A-Za-z0-9_]*=)[^[:space:]]+/\1<redacted>/g' \
        -e '/-----BEGIN [A-Z ]*PRIVATE KEY-----/,/-----END [A-Z ]*PRIVATE KEY-----/c\
<redacted private key>' \
        -e '/-----BEGIN [A-Z ]*PRIVATE KEY-----/,$c\
<redacted private key>'
}
