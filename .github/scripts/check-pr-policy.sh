#!/usr/bin/env bash
set -eu

APP_AUTHOR='delegatd-agent-1352096391[bot]'
DEPENDABOT_AUTHOR='dependabot[bot]'

reject() {
  printf 'PR policy: %s\n' "$1" >&2
  exit 1
}

strip_html_comments() {
  awk '
    BEGIN { in_comment = 0 }
    {
      line = $0
      output = ""
      while (length(line) > 0 || in_comment) {
        if (in_comment) {
          close_at = index(line, "-->")
          if (close_at == 0) {
            line = ""
            break
          }
          line = substr(line, close_at + 3)
          in_comment = 0
          continue
        }

        open_at = index(line, "<!--")
        if (open_at == 0) {
          output = output line
          line = ""
          break
        }
        output = output substr(line, 1, open_at - 1)
        line = substr(line, open_at + 4)
        in_comment = 1
      }
      print output
    }
  ' "$1" > "$2"
}

validate_body() {
  body_path=$1
  issue_number=$2

  if ! awk 'index($0, "REPLACE_ME") { found = 1 } END { exit found ? 1 : 0 }' "$body_path"; then
    reject 'replace all visible REPLACE_ME sentinels'
  fi

  if ! awk 'index($0, "make verify") { found = 1 } END { exit found ? 0 : 1 }' "$body_path"; then
    reject 'include the literal make verify command'
  fi

  if ! awk '
    BEGIN {
      expected[1] = "## Summary"
      expected[2] = "## Linear issue"
      expected[3] = "## Observable behavior"
      expected[4] = "## Verification"
      expected[5] = "## Provenance"
      expected[6] = "## Security and operations"
      next_heading = 1
      content = 0
      bad = 0
    }
    {
      line = $0
      sub(/\r$/, "", line)
      if (line ~ /^##[[:space:]]/) {
        heading = line
        sub(/[[:space:]]+$/, "", heading)
        if (next_heading > 6 || heading != expected[next_heading]) {
          bad = 1
          exit
        }
        if ((next_heading == 1 && content) || (next_heading > 1 && !content)) {
          bad = 1
          exit
        }
        next_heading++
        content = 0
      } else if (line ~ /[^[:space:]]/) {
        content = 1
      }
    }
    END {
      if (bad || next_heading != 7 || !content) {
        exit 1
      }
      exit 0
    }
  ' "$body_path"; then
    reject 'use each required heading exactly once, in order, with content'
  fi

  origin_lines=$(awk '/^[[:space:]]*Origin:/ { count++ } END { print count + 0 }' "$body_path")
  origin_matches=$(awk '/^[[:space:]]*Origin: (agent|human|mixed)[[:space:]]*$/ { count++ } END { print count + 0 }' "$body_path")
  if [ "$origin_lines" -ne 1 ] || [ "$origin_matches" -ne 1 ]; then
    reject 'include exactly one valid Origin: agent, Origin: human, or Origin: mixed line'
  fi

  close_result=$(awk '
    {
      line = $0
      prefix = "Closes POU-"
      while ((start = index(line, prefix)) > 0) {
        if (start == 1) {
          before = ""
        } else {
          before = substr(line, start - 1, 1)
        }
        remainder = substr(line, start + length(prefix))
        if (remainder !~ /^[0-9]/) {
          invalid = 1
          line = substr(line, start + length(prefix))
          continue
        }
        match(remainder, /^[0-9]+/)
        close_number = substr(remainder, RSTART, RLENGTH)
        after = substr(remainder, RLENGTH + 1, 1)
        if ((before != "" && before ~ /[[:alnum:]_]/) || (after != "" && after ~ /[[:alnum:]_]/)) {
          invalid = 1
        }
        count++
        if (first == "") {
          first = close_number
        }
        line = substr(line, start + length(prefix) + RLENGTH)
      }
    }
    END { printf "%d\t%s\t%d\n", count + 0, first, invalid + 0 }
  ' "$body_path")
  close_count=$(printf '%s\n' "$close_result" | cut -f1)
  close_issue=$(printf '%s\n' "$close_result" | cut -f2)
  close_invalid=$(printf '%s\n' "$close_result" | cut -f3)
  if [ "$close_invalid" -ne 0 ] || [ "$close_count" -ne 1 ]; then
    reject 'include exactly one uppercase Closes POU-<number> reference'
  fi
  if [ "$close_issue" != "$issue_number" ]; then
    reject "Closes POU-$issue_number must match the branch issue key"
  fi
}

self_test() {
  script_path=$0
  fixture_body='## Summary
Implement the repository gate.

## Linear issue
Closes POU-42

## Observable behavior
Unsafe changes fail closed.

## Verification
make verify

## Provenance
Origin: agent
Identity: delegatd-agent-1352096391

## Security and operations
No new privileged effect; rollback by revert.'

  run_case() {
    case_name=$1
    expected=$2
    event_name=$3
    author=$4
    head_ref=$5
    body=$6
    if GITHUB_EVENT_NAME="$event_name" PR_AUTHOR="$author" PR_HEAD_REF="$head_ref" PR_BODY="$body" "$script_path" >/dev/null 2>&1; then
      status=0
    else
      status=$?
    fi
    if [ "$expected" = pass ] && [ "$status" -ne 0 ]; then
      printf 'check-pr-policy self-test: %s unexpectedly rejected\n' "$case_name" >&2
      exit 1
    fi
    if [ "$expected" = reject ] && [ "$status" -eq 0 ]; then
      printf 'check-pr-policy self-test: %s unexpectedly accepted\n' "$case_name" >&2
      exit 1
    fi
  }

  run_case 'accepted App' pass pull_request "$APP_AUTHOR" 'feature/pou-42-repository-gate' "$fixture_body"
  run_case 'HTML comments ignored' pass pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "<!-- REPLACE_ME guidance -->
$fixture_body"
  run_case 'narrow Dependabot exemption' pass pull_request "$DEPENDABOT_AUTHOR" 'dependency-update' 'Dependabot body is exempt from branch and body rules.'
  run_case 'non-pull-request bypass' pass push '' '' ''
  run_case 'owner author rejected' reject pull_request 'poulsena' 'pou-42-repository-gate' "$fixture_body"
  run_case 'mismatched Linear key rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$(printf '%s\n' "$fixture_body" | sed 's/Closes POU-42/Closes POU-43/')"
  run_case 'duplicate Linear key rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$(printf '%s\n' "$fixture_body" | sed 's/Closes POU-42/Closes POU-42;Closes POU-42/')"
  run_case 'missing heading rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$(printf '%s\n' "$fixture_body" | sed '/^## Observable behavior$/d')"
  run_case 'duplicate heading rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$fixture_body
## Summary
duplicate section"
  run_case 'reordered heading rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' '## Summary
Implement the repository gate.

## Observable behavior
Unsafe changes fail closed.

## Linear issue
Closes POU-42

## Verification
make verify

## Provenance
Origin: agent

## Security and operations
No new privileged effect.'
  run_case 'empty heading rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$(printf '%s\n' "$fixture_body" | sed '/^Implement the repository gate\.$/d')"
  run_case 'comment-only body rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' '<!-- guidance only -->'
  run_case 'visible sentinel rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$fixture_body
REPLACE_ME"
  run_case 'missing make verify rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$(printf '%s\n' "$fixture_body" | sed '/^make verify$/d')"
  run_case 'duplicate provenance rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$fixture_body
Origin: human"
  run_case 'missing provenance rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' "$(printf '%s\n' "$fixture_body" | sed '/^Origin: agent$/d')"
  run_case 'empty author rejected' reject pull_request '' 'pou-42-repository-gate' "$fixture_body"
  run_case 'empty branch rejected' reject pull_request "$APP_AUTHOR" '' "$fixture_body"
  run_case 'empty body rejected' reject pull_request "$APP_AUTHOR" 'pou-42-repository-gate' ''

  printf '%s\n' 'check-pr-policy self-test: passed'
}

if [ "${1-}" = '--self-test' ]; then
  self_test
  exit 0
fi

if [ "${GITHUB_EVENT_NAME-}" != 'pull_request' ]; then
  exit 0
fi

author=${PR_AUTHOR-}
head_ref=${PR_HEAD_REF-}
body=${PR_BODY-}
[ -n "$author" ] || reject 'PR_AUTHOR is required'
[ -n "$head_ref" ] || reject 'PR_HEAD_REF is required'
[ -n "$body" ] || reject 'PR_BODY is required'

if [ "$author" = "$DEPENDABOT_AUTHOR" ]; then
  exit 0
fi
[ "$author" = "$APP_AUTHOR" ] || reject 'PR author must be the dedicated agent App'

if ! printf '%s\n' "$head_ref" | grep -E -q '^[^[:space:]]+$'; then
  reject 'PR_HEAD_REF must be a single non-whitespace branch name'
fi
if ! printf '%s\n' "$head_ref" | grep -E -q '(^|/)pou-[0-9]+-'; then
  reject 'PR_HEAD_REF must contain (^|/)pou-<number>-'
fi
issue_number=$(printf '%s\n' "$head_ref" | sed -E 's#(^|.*/)pou-([0-9]+)-.*#\2#')

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/delegatd-pr-policy.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM
printf '%s\n' "$body" > "$tmp_dir/body"
strip_html_comments "$tmp_dir/body" "$tmp_dir/clean"
validate_body "$tmp_dir/clean" "$issue_number"
