#!/usr/bin/env bash
# The PR semantics gate: the repository's PR contract, which every pull
# request into main is held to, so that what its squash merge leaves on main
# tells release-please the truth:
#
#   1. The PR title is a Conventional Commit header: the squash merge gives
#      main a commit with that header, and release-please versions the
#      release and writes the changelog from it.
#   2. Every commit in the PR is a Conventional Commit (merge commits are
#      exempt; fixup!/squash! commits must be autosquashed before merging).
#   3. The title's release impact EQUALS the highest release impact of the
#      commits: the title may neither understate the release (a fix: title
#      over a feat: commit) nor promise one the commits do not make (a feat:
#      title over docs-only commits). The squash commit's body keeps the
#      commits' messages, BREAKING CHANGE footers included.
#
# Impact classes, as release-please-config.json releases them:
#   major = "!" after the type/scope, or a "BREAKING CHANGE:" footer
#   minor = feat
#   patch = fix, revert
#   none  = docs, style, refactor, perf, test, build, ci, chore
# (a perf or refactor that must ship a release is a fix).
#
# Environment:
#   REPO       owner/name of the repository
#   PR_NUMBER  the pull request's number
#   PR_TITLE   its title, passed as an environment variable and never
#              interpolated into the script; read from the PR when empty
#   GH_TOKEN   a token able to read the pull request
set -euo pipefail

: "${REPO:?}" "${PR_NUMBER:?}" "${GH_TOKEN:?}"

# gh sets no request timeout, and GitHub's API frontends do stall now and
# then: each attempt is bounded, and a failed one retried twice.
gh_read() {
    local attempt out
    for attempt in 1 2 3; do
        if out="$(timeout 30 gh "$@")"; then
            printf '%s' "$out"
            return 0
        fi
        (( attempt < 3 )) && sleep $(( attempt * 5 ))
    done
    return 1
}
command -v timeout >/dev/null || timeout() { shift; "$@"; }

TYPES='feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert'
HEADER_RE="^(${TYPES})(\([^)]+\))?(!)?: .+"

impact() { # $1 = headline, $2 = body → major|minor|patch|none; "" = not conventional
    local headline="$1" body="${2:-}" type
    [[ "$headline" =~ $HEADER_RE ]] || { echo ""; return; }
    type="${BASH_REMATCH[1]}"
    if [[ -n "${BASH_REMATCH[3]}" ]] || grep -qE '^BREAKING[ -]CHANGE:' <<<"$body"; then
        echo major; return
    fi
    case "$type" in
        feat)       echo minor ;;
        fix|revert) echo patch ;;
        *)          echo none ;;
    esac
}

rank() { case "$1" in major) echo 3 ;; minor) echo 2 ;; patch) echo 1 ;; *) echo 0 ;; esac; }

if [[ -z "${PR_TITLE:-}" ]]; then
    PR_TITLE="$(gh_read pr view "$PR_NUMBER" -R "$REPO" --json title --jq .title)"
fi

errors=()

title_impact="$(impact "$PR_TITLE" "")"
if [[ -z "$title_impact" ]]; then
    errors+=("the PR title is not a Conventional Commit header: \"${PR_TITLE}\"")
fi

# gh pr view returns at most 100 commits: enough for any reviewable PR, and a
# PR over the limit fails below rather than passing half-checked.
commits_json="$(gh_read pr view "$PR_NUMBER" -R "$REPO" --json commits --jq '.commits')"
count="$(jq 'length' <<<"$commits_json")"
if (( count >= 100 )); then
    errors+=("the PR has ${count}+ commits; the gate checks only the first 100: split the PR")
fi

max=none
while IFS= read -r row; do
    headline="$(jq -r '.messageHeadline' <<<"$row")"
    body="$(jq -r '.messageBody' <<<"$row")"
    sha="$(jq -r '.oid' <<<"$row" | cut -c1-7)"
    [[ "$headline" == "Merge "* ]] && continue
    commit_impact="$(impact "$headline" "$body")"
    if [[ -z "$commit_impact" ]]; then
        errors+=("commit ${sha} is not a Conventional Commit: \"${headline}\"")
        continue
    fi
    if (( $(rank "$commit_impact") > $(rank "$max") )); then max="$commit_impact"; fi
done < <(jq -c '.[]' <<<"$commits_json")

# The equality verdict means something only once the title and every commit parse.
if [[ -n "$title_impact" ]] && (( ${#errors[@]} == 0 )) && [[ "$title_impact" != "$max" ]]; then
    errors+=("release impact mismatch: the title says ${title_impact}, the commits compute ${max}; retitle the PR (or fix the commits) so that both agree")
fi

if (( ${#errors[@]} > 0 )); then
    {
        echo "PR semantics gate failed for ${REPO}#${PR_NUMBER}:"
        printf ' ✘ %s\n' "${errors[@]}"
        cat <<'DOC'

The contract (AGENTS.md, "Git and commits"):
  · title and every commit:  <type>[(scope)][!]: <subject>
    types: feat fix docs style refactor perf test build ci chore revert
  · release impact of the title == highest release impact of the commits
    major = "!" or a "BREAKING CHANGE:" footer · minor = feat · patch = fix, revert · none = the rest
  · merge commits are exempt; autosquash fixup!/squash! before merging
DOC
    } | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}" >&2
    exit 1
fi

echo "✓ ${REPO}#${PR_NUMBER}: title impact = ${title_impact}, the commits agree (${count} commits)" \
    | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
