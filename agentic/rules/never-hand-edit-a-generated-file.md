# Never hand-edit a generated file

`.github/dependabot.yml`, the gt workflows and the branch-protection rules are rendered from
`.gt-repo.yaml`. Edit that file and run `gt repo sync`. A hand edit is overwritten by the next
sync, silently, and the change reads as having been reverted by nobody.
