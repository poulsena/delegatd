#!/usr/bin/env bash
set -eu

reject() {
  printf 'release tag verification: %s\n' "$1" >&2
  exit 1
}

verify_tag() {
  tag=$1
  main_ref=$2
  [ -n "$tag" ] || reject 'tag is required'
  [ -n "$main_ref" ] || reject 'main reference is required'
  if ! [[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    reject "tag $tag is not strict semver"
  fi

  repo_root=$(git rev-parse --show-toplevel 2>/dev/null) || reject 'not inside a Git repository'
  allowed_relative='.github/release-allowed-signers'
  allowed_signers="$repo_root/$allowed_relative"
  [ -f "$allowed_signers" ] || reject 'tracked SSH allowed-signers file is missing'
  git -C "$repo_root" ls-files --error-unmatch -- "$allowed_relative" >/dev/null 2>&1 || reject 'SSH allowed-signers file is not tracked'

  tag_ref="refs/tags/$tag"
  git -C "$repo_root" show-ref --verify --quiet "$tag_ref" || reject "tag $tag is not present"
  object_type=$(git -C "$repo_root" cat-file -t "$tag_ref" 2>/dev/null) || reject "tag $tag cannot be inspected"
  [ "$object_type" = tag ] || reject "tag $tag must be annotated"

  git -C "$repo_root" -c gpg.format=ssh -c gpg.ssh.allowedSignersFile="$allowed_signers" tag -v "$tag" >/dev/null 2>&1 || reject "tag $tag is not signed by an allowed key"
  peeled_commit=$(git -C "$repo_root" rev-parse --verify "$tag_ref^{}" 2>/dev/null) || reject "tag $tag does not peel to a commit"
  head_commit=$(git -C "$repo_root" rev-parse --verify HEAD 2>/dev/null) || reject 'checked-out HEAD cannot be resolved'
  [ "$peeled_commit" = "$head_commit" ] || reject "tag $tag does not point at checked-out HEAD"
  main_commit=$(git -C "$repo_root" rev-parse --verify "$main_ref^{commit}" 2>/dev/null) || reject "main reference $main_ref cannot be resolved"
  git -C "$repo_root" merge-base --is-ancestor "$peeled_commit" "$main_commit" || reject "tag $tag is not an ancestor of $main_ref"
  printf 'verified %s at %s\n' "$tag" "$peeled_commit"
}

self_test() {
  script_path=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/$(basename -- "$0")
  temp_root=$(mktemp -d "${TMPDIR:-/tmp}/delegatd-release-tag.XXXXXX")
  trap 'rm -rf "$temp_root"' EXIT HUP INT TERM
  owner_key="$temp_root/owner"
  other_key="$temp_root/other"
  ssh-keygen -q -t ed25519 -N '' -C owner@example.test -f "$owner_key" >/dev/null
  ssh-keygen -q -t ed25519 -N '' -C other@example.test -f "$other_key" >/dev/null
  owner_public=$(cat "$owner_key.pub")

  setup_repo() {
    repo=$1
    mkdir -p "$repo/.github"
    git -C "$repo" init -q
    git -C "$repo" symbolic-ref HEAD refs/heads/main
    git -C "$repo" config user.name 'Release Test'
    git -C "$repo" config user.email owner@example.test
    printf 'base\n' > "$repo/README"
    printf 'owner@example.test %s\n' "$owner_public" > "$repo/.github/release-allowed-signers"
    git -C "$repo" add README .github/release-allowed-signers
    git -C "$repo" commit -qm base
  }

  sign_tag() {
    repo=$1
    tag=$2
    key=$3
    git -C "$repo" -c gpg.format=ssh -c user.signingkey="$key" tag -s -m release "$tag"
  }

  expect_success() {
    repo=$1
    tag=$2
    main_ref=$3
    if ! (cd "$repo" && "$script_path" "$tag" "$main_ref" >/dev/null 2>&1); then
      printf 'verify-release-tag self-test: %s unexpectedly rejected\n' "$tag" >&2
      exit 1
    fi
  }

  expect_rejection() {
    repo=$1
    tag=$2
    main_ref=$3
    if (cd "$repo" && "$script_path" "$tag" "$main_ref" >/dev/null 2>&1); then
      printf 'verify-release-tag self-test: %s unexpectedly accepted\n' "$tag" >&2
      exit 1
    fi
  }

  valid_repo="$temp_root/valid"
  setup_repo "$valid_repo"
  sign_tag "$valid_repo" v0.0.0 "$owner_key"
  expect_success "$valid_repo" v0.0.0 main

  lightweight_repo="$temp_root/lightweight"
  setup_repo "$lightweight_repo"
  git -C "$lightweight_repo" tag v0.0.1
  expect_rejection "$lightweight_repo" v0.0.1 main

  wrong_signer_repo="$temp_root/wrong-signer"
  setup_repo "$wrong_signer_repo"
  sign_tag "$wrong_signer_repo" v0.0.2 "$other_key"
  expect_rejection "$wrong_signer_repo" v0.0.2 main

  malformed_repo="$temp_root/malformed"
  setup_repo "$malformed_repo"
  sign_tag "$malformed_repo" v01.0.0 "$owner_key"
  expect_rejection "$malformed_repo" v01.0.0 main

  wrong_head_repo="$temp_root/wrong-head"
  setup_repo "$wrong_head_repo"
  sign_tag "$wrong_head_repo" v0.0.3 "$owner_key"
  printf 'later\n' >> "$wrong_head_repo/README"
  git -C "$wrong_head_repo" add README
  git -C "$wrong_head_repo" commit -qm later
  expect_rejection "$wrong_head_repo" v0.0.3 main

  off_main_repo="$temp_root/off-main"
  setup_repo "$off_main_repo"
  git -C "$off_main_repo" checkout -q -b off-main
  printf 'side\n' >> "$off_main_repo/README"
  git -C "$off_main_repo" add README
  git -C "$off_main_repo" commit -qm side
  sign_tag "$off_main_repo" v0.0.4 "$owner_key"
  expect_rejection "$off_main_repo" v0.0.4 main

  printf '%s\n' 'verify-release-tag self-test: passed'
}

if [ "${1-}" = '--self-test' ]; then
  self_test
  exit 0
fi

[ "$#" -eq 2 ] || reject 'usage: verify-release-tag.sh TAG MAIN_REF'
verify_tag "$1" "$2"
