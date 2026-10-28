# Release workflow

One milestone = one release. Versions follow
[Semantic Versioning](https://semver.org/) and the changelog follows
[Keep a Changelog](https://keepachangelog.com/). Tags are `vX.Y.Z`.

Everything is written in **English**, including release notes.

## 1. Release gate

A release is ready when the milestone has **no open issues**:

```bash
gh api repos/{owner}/{repo}/milestones --jq '.[] | select(.title=="vX.Y.Z") | {title, open_issues, closed_issues}'
```

- Open issues that will not make it: move them to the next milestone
  (`gh issue edit <N> --milestone vX.Y.(Z+1)`).
- The default branch must have a green `ci-ok`.

## 2. Choose the version

| Change | Bump |
|---|---|
| Bug fixes only | patch (`1.2.3` → `1.2.4`) |
| New, backward compatible features | minor (`1.2.3` → `1.3.0`) |
| Breaking changes | major (`1.2.3` → `2.0.0`) |

Before `1.0.0`, breaking changes bump the minor version. The milestone title
already holds the planned version. Change the milestone title if the plan changed.

## 3. Prepare the CHANGELOG (pull request)

```bash
git switch -c chore/release-vX.Y.Z
```

In `CHANGELOG.md`:

- Rename `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`.
- Add a fresh empty `## [Unreleased]` above it.
- Group entries under Added, Changed, Deprecated, Removed, Fixed, Security.
- Update the compare links at the bottom, if the file has them.
- There is no version constant to bump: the version comes from the tag.

Open a PR titled `chore: release vX.Y.Z`, wait for `ci-ok`, squash merge
(`gh pr merge --squash --delete-branch`).

## 4. Tag

Tag the merge commit on the default branch with an **annotated** tag:

```bash
git switch <default-branch> && git pull --ff-only
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

## 5. GitHub Release

Pushing the tag starts `.github/workflows/release.yaml`. It is self-contained: it builds
the four `tyci_<os>_<arch>` binaries (linux and darwin, amd64 and arm64), creates the
GitHub Release with the notes from the matching `CHANGELOG.md` section, and attaches the
binaries and a `checksums.txt` file. It fails when the section is missing. The workflow
reads `CHANGELOG.md` from the tagged commit, so the release PR with the `## [X.Y.Z]`
section must be **merged before** you tag. Tags with a `-` (for example `v0.1.0-rc.1`)
become pre-releases.

```bash
gh run watch
gh release view vX.Y.Z
```

## 6. Close the milestone

```bash
gh api -X PATCH repos/{owner}/{repo}/milestones/<number> -f state=closed
```

Make sure the next milestone `vX.Y.(Z+1)` (or the next minor) exists.

## 7. After the release

- Check that install instructions work with the new version (release binaries and `checksums.txt`).
- If something is wrong, do not move the tag. Fix forward with a patch release.

## Automated release

The workflow `release` runs steps 1 to 6 for you. The manual steps above remain the reference.

1. Run `tyci workflow init release` in the repository.
2. Run `workflow_start release v0.10.0` with the milestone title.
3. Wait for the question `ask_release`. The run checks the milestone and CI first.
4. Answer `yes` to release, or `no` to stop. A `no` changes nothing on GitHub.

After a `yes`, the run does these steps:

- The worker moves `[Unreleased]` to `[X.Y.Z] - date` in CHANGELOG.md and opens a pull request.
- The run waits for CI, then merges the pull request.
- The run creates an annotated tag `X.Y.Z` on the merge commit and pushes it.
- The run waits for the release workflow and checks the binaries and `checksums.txt`.
- The run closes the milestone.

The `release` workflow never deletes or moves a tag. If a tag already exists, the run stops. Fix the problem by hand, then decide the next version.

If the run pauses with a question after the CHANGELOG pull request is merged, answer with a `goto` form:

- If the CHANGELOG pull request is merged and the tag is not yet created, answer `goto tag`.
- If the release tag is already on origin, answer `goto wait`.

Do not answer `retry` after the merge. The `[Unreleased]` section is empty then, so the worker cannot make a new CHANGELOG change.

## Checklist

- [ ] Milestone has no open issues, CI is green
- [ ] CHANGELOG section `[X.Y.Z] - date` written, `[Unreleased]` is empty
- [ ] Release PR merged
- [ ] Annotated tag `vX.Y.Z` pushed
- [ ] GitHub Release exists with the CHANGELOG notes and the four binaries
- [ ] Milestone closed, next milestone exists
