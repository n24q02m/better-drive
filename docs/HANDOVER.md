# HANDOVER

## Status: maintenance mode — planned archival

This project is in maintenance mode and is planned for archival once consumers have migrated to maintained, official alternatives. **No new features will be added. Dependency updates are frozen.** Only a critical security fix would trigger a further release before archival.

**Guidance: prefer official, first-party tools over configuring this project.** For Google Drive and other cloud-storage synchronization, use [rclone](https://rclone.org/) directly — its official documentation covers remote configuration, scheduled sync/move jobs, and integrity checking (`rclone check`). Wrapping day-to-day sync workflows in this repository's own scheduler and OAuth setup is no longer the recommended path.

## Current release

The latest release is published on the [GitHub Releases page](https://github.com/n24q02m/better-drive/releases). Release provenance note for v1.9.1: this version was published by the release pipeline from a routine dependency-fix merge on the default branch rather than from a dedicated release line; content-wise it matches the current main branch.

## What remains operational until archival

- Already-installed scheduled jobs continue running on installed machines until each is individually replaced and disabled.
- No fresh installs, account connections, or new schedules should be set up.

## Migration notes

1. List every scheduled job (source, destination, cadence, filters).
2. Recreate each job as an rclone command; start with `--dry-run` to preview, then run one live cycle and compare file counts and checksums.
3. Keep each old job disabled (not deleted) until its replacement has completed at least one verified cycle.
4. After all jobs are verified, uninstall the application and remove the scheduled tasks.
5. Local fixture/quarantine folders used for testing should be reviewed and removed during the same cleanup.

## After archival

The repository will become read-only. Existing releases remain downloadable, but no further builds, releases, or support responses should be expected.
