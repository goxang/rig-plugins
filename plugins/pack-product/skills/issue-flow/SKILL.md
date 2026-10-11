---
name: issue-flow
description: create issues for someone, do an issue (or all of mine) on a branch, open its MR, and link issue and MR both ways
---

# From issue to merge request

Find the actions with rig_find (tracker, scm); call them with rig_call. Writes ask the user first.

## Create an issue for someone

`tracker.create` with `summary`, `description` (acceptance criteria as a list) and `assignee` (a name,
an email or `me`). Reply with the key and its URL.

## Do an issue

1. `tracker.issue key: KEY` for the details; ask only what the issue leaves open.
2. Branch from the default branch: `git switch -c KEY-short-slug` (the key leads, so rig links it).
3. Implement, run the project's tests, commit (`KEY: what changed`), `git push -u origin HEAD`.
4. `tracker.update key: KEY append: "branch: KEY-short-slug"`.
5. `scm.open source: KEY-short-slug title: "KEY summary" body: <what changed, how it was tested, the issue URL>`.
6. `tracker.update key: KEY append: "MR: <url>"`, then `tracker.transition key: KEY status: "In Review"`
   (if the status does not exist, say which ones do and stop there).

"Do all my tasks": `tracker.issues assignee: me`, skip Done ones, list them with a plan and do them one
by one, each on its own branch and MR. Stop at the first that fails and report.

## Open an MR for an issue

The work is already pushed: find its branch (the issue's description, or `git branch -r --list '*KEY*'`),
then steps 5 and 6.

## Between them

An MR's issue is the key in its title or branch (the `issue` column of `scm.mrs`); an issue's MR is the
`MR:` line of its description. In the UI: `i` on an MR opens its issue, `o` on an issue its MR.
