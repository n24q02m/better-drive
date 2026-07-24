# CHANGELOG

<!-- version list -->

## v1.5.0-beta.1 (2026-07-24)

### Bug Fixes

- Configure Renovate with SHA pinning and rate limits
  ([#1](https://github.com/n24q02m/better-drive/pull/1),
  [`63e5202`](https://github.com/n24q02m/better-drive/commit/63e52021f49af796df7e0daca2ef7d9a6691a66b))

- Pin GitHub Action references to commit SHAs ([#8](https://github.com/n24q02m/better-drive/pull/8),
  [`cd0a822`](https://github.com/n24q02m/better-drive/commit/cd0a8227748a34aa2b99eefc73d2c8b79b98d88d))

### Chores

- Configure Renovate ([#1](https://github.com/n24q02m/better-drive/pull/1),
  [`63e5202`](https://github.com/n24q02m/better-drive/commit/63e52021f49af796df7e0daca2ef7d9a6691a66b))

### Features

- Show dynamic sync status in system tray tooltip and disable invalid actions
  ([`a70278e`](https://github.com/n24q02m/better-drive/commit/a70278e3512a36cf0a87dd7f07b78899a33bcbe8))


## v1.4.0 (2026-07-19)

### Bug Fixes

- Keep macOS build headless (defer cgo tray), correct README to Windows/Linux tray
  ([`eb6a974`](https://github.com/n24q02m/better-drive/commit/eb6a97482d4a106d7d7c7e0015cc565374bfb628))

- Refresh README for shell-out rclone engine + cross-platform install
  ([`c545c44`](https://github.com/n24q02m/better-drive/commit/c545c44ed297d0e81767dac3585116de18e01485))

- Run rclone with CREATE_NO_WINDOW so the GUI daemon stops flashing console windows
  ([`9dbaadb`](https://github.com/n24q02m/better-drive/commit/9dbaadb855d2d568ab31874a5e0e1ed39f7bf666))

### Features

- Document macOS system-tray support in README
  ([`c552af2`](https://github.com/n24q02m/better-drive/commit/c552af230d1e61309e8af5758074886482ea4eb1))

- Enable systray daemon on linux and macOS
  ([`188c3eb`](https://github.com/n24q02m/better-drive/commit/188c3eb3f7f03d5c3ad4737425aeba4fe1848682))


## v1.3.0 (2026-07-19)

### Features

- Add cross-platform builds, homebrew tap, and rclone dep to goreleaser
  ([`42c2ba1`](https://github.com/n24q02m/better-drive/commit/42c2ba1e872362af164c6a8b497d74a05fbf9794))

- Add dependency-review and installer-smoke CI jobs
  ([`87a85bb`](https://github.com/n24q02m/better-drive/commit/87a85bb8f447a47c84afc2ae4b0a3365dcb192d2))

- Add exec-based runner seam to engine for rclone shell-out
  ([`2ee04f1`](https://github.com/n24q02m/better-drive/commit/2ee04f1dde454569327eff3d5300592b11dc3ab4))

- Add one-shot install.ps1/install.sh installers
  ([`ef50451`](https://github.com/n24q02m/better-drive/commit/ef504518a4382fa96cef1b69a68d3ac565bd9475))

- Add OpenSSF Scorecard workflow
  ([`0531333`](https://github.com/n24q02m/better-drive/commit/053133345824ef2cb7bfb0f5c8d5b8fef259adc5))

- Add real darwin/linux autostart implementations
  ([`9c08be3`](https://github.com/n24q02m/better-drive/commit/9c08be310a163c0aae1a586b9604f79ccf49e010))

- Purge librclone dependency, drop rclone from go.mod
  ([`5a0f184`](https://github.com/n24q02m/better-drive/commit/5a0f184ea00b47b3a34092cab1e1ef2aadb3e9c1))

- Shell out Bisync to rclone bisync, keep ErrNeedsResync mapping
  ([`2858b60`](https://github.com/n24q02m/better-drive/commit/2858b603643fd506a7f15b510585af21791ba14a))

- Shell out Copy/Sync to rclone copy/sync/copyto
  ([`0ed88d9`](https://github.com/n24q02m/better-drive/commit/0ed88d9ba2f6dc7baa1cdab170f9274c2bbcb643))

- Shell out remote/config methods to rclone config/listremotes/lsf
  ([`466ac4a`](https://github.com/n24q02m/better-drive/commit/466ac4afd63cf8825194c1e16d405e8a7093070a))

- Split tray package by build tag for cgo-free non-windows builds
  ([`f7e899f`](https://github.com/n24q02m/better-drive/commit/f7e899fcac1fa6591350dfe523b04e8d3b27a91b))


## v1.2.0 (2026-07-19)

### Features

- Log each sync cycle's outcome to a persistent log file
  ([`22609c9`](https://github.com/n24q02m/better-drive/commit/22609c9632a865abce316afded7356e1ad3a60a1))


## v1.1.0 (2026-07-18)

### Bug Fixes

- Add non-windows autostart stub so Linux CI build passes
  ([`e691ddc`](https://github.com/n24q02m/better-drive/commit/e691ddc9fbf1c846735203e48a0eb41e0ac6922f))

- Adopt better-semantic-release for built-in release guards
  ([`758e961`](https://github.com/n24q02m/better-drive/commit/758e9611d41c1647dcc194cb9fff4e68e882aea7))

- Config path env override so status test is env-independent (CI Linux passed)
  ([`e828bf7`](https://github.com/n24q02m/better-drive/commit/e828bf7a592ad0bd09e52dfdafa56ebc85ed43c9))

- Honor RCLONE_CONFIG and retry transient Drive errors in backup
  ([`a630e1d`](https://github.com/n24q02m/better-drive/commit/a630e1d574babbee48d064d7913cb3e8bb4fc647))

- Keep redirected stdout in windowsgui build so `sync > log` captures output
  ([`ff99e22`](https://github.com/n24q02m/better-drive/commit/ff99e228ccadaa43d40bb1d2b90288a63d3cc350))

- Serialize sync ops (engine mutex) — concurrent copy/bisync race rclone global _filter
  ([`fb4c0dc`](https://github.com/n24q02m/better-drive/commit/fb4c0dccdc11ab8cdc15f75a04b05368b7079c31))

- Tolerate live-file errors and skip missing local sources in backup
  ([`8f4047f`](https://github.com/n24q02m/better-drive/commit/8f4047ffeca62f8fbd2cc38e167752e975db058e))

- Use no_check_updated for live-directory backup, drop wrong IgnoreErrors
  ([`93bbcc0`](https://github.com/n24q02m/better-drive/commit/93bbcc0d6edfa342e5e3c75ecf922588cc386795))

### Features

- Accept N config pairs and add per-pair exclude patterns
  ([`4f13586`](https://github.com/n24q02m/better-drive/commit/4f1358645a5cb4dbb78d9bc1ee99dda11356104c))

- Add engine.Copy and engine.Sync for 1-way rclone modes
  ([`c5cf519`](https://github.com/n24q02m/better-drive/commit/c5cf5192cd32941d4113da008a5f0c5bb59064f9))

- Add mode field to Pair config (bisync/copy/sync)
  ([`cc244eb`](https://github.com/n24q02m/better-drive/commit/cc244eb4bb5fbf3848cd4e60fb6103c4757a0434))

- Add tray Aggregator to combine per-pair sync state
  ([`ba2e3c1`](https://github.com/n24q02m/better-drive/commit/ba2e3c10470621776918a754742d0394f5d44995))

- Autostart via HKCU Run key
  ([`a2183ab`](https://github.com/n24q02m/better-drive/commit/a2183ab7e4c3583b12bb75bea60e5de929f4f139))

- Better-drive install/uninstall + internal rclone config path
  ([`6da8c65`](https://github.com/n24q02m/better-drive/commit/6da8c65c72489023d5c818cfc59315bc4c4b9906))

- Dispatch syncloop by mode (bisync/copy/sync) and thread mode from cli
  ([`bf8b4d7`](https://github.com/n24q02m/better-drive/commit/bf8b4d7efc5a023e238a3f21303db096fc25652b))

- Document multi-pair config, sync modes, and config excludes
  ([`96d04f2`](https://github.com/n24q02m/better-drive/commit/96d04f24fda37210b041df2dc771d572357ba0e3))

- Engine sync-op serialization regression test
  ([`ff30a7b`](https://github.com/n24q02m/better-drive/commit/ff30a7b4a2720c35dcd383ff645295712a62ee89))

- Engine.New sources rclone config path from arg then env
  ([`c2982a1`](https://github.com/n24q02m/better-drive/commit/c2982a11f61520595524fea1e86310c261c01c75))

- Extract TranslateIgnoreLines and add PairFilters for config excludes
  ([`343f739`](https://github.com/n24q02m/better-drive/commit/343f73999b997ee0c81ec3d48914817824f76c17))

- Fast-list + tuned transfers on sync ops (large-folder backup speed)
  ([`3cf4444`](https://github.com/n24q02m/better-drive/commit/3cf4444f0deafde01074fbe237b5501803273bcf))

- GUI-subsystem build with parent-console attach for CLI output
  ([`476af12`](https://github.com/n24q02m/better-drive/commit/476af124a36b10b1208849510b326120dacc288a))

- One-shot sync command and Loop.RunOnce for scheduled backups
  ([`50a3b48`](https://github.com/n24q02m/better-drive/commit/50a3b488e728a35e0ab986eb8ce83e4ce4b68d1c))

- Resolve rclone config path from config field + auto-detect
  ([`bac00c2`](https://github.com/n24q02m/better-drive/commit/bac00c26000e0ca486687c4801d21df118e5d40c))

- Run one syncloop per config pair with combined tray status
  ([`5452133`](https://github.com/n24q02m/better-drive/commit/545213369a90cd433e4e3b5a6055fa62021ff44b))

- Single-file source support in engine Copy/Sync (operations/copyfile)
  ([`29204e8`](https://github.com/n24q02m/better-drive/commit/29204e84bb8db152334e37025ae4f8159dc42da2))


## v1.0.0 (2026-07-18)

### Bug Fixes

- Mark beta/rc goreleaser releases as prerelease (auto)
  ([`4d70e3f`](https://github.com/n24q02m/better-drive/commit/4d70e3ff67dd11514bdd48d341c33780ed024ac8))


## v1.0.0-beta.1 (2026-07-17)

- Initial Release
