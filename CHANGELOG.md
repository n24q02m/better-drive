# CHANGELOG

<!-- version list -->

## v1.7.0 (2026-08-25)

### Bug Fixes

- Align Go floor across CI platforms ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Bind cleanup intents to canonical approvals
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Bind restore floors to provider ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Close final backup safety gaps ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Consolidate BDrive verification gates ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Document cleanup binding flags ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Fail closed on unsupported child image platforms
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Gate unsupported Darwin runtime paths ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Harden release publication policy ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Isolate mutable rclone transfer config ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Make runtime fixtures portable across CI hosts
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Push release commit with the CI App identity
  ([#140](https://github.com/n24q02m/better-drive/pull/140),
  [`cf5e67b`](https://github.com/n24q02m/better-drive/commit/cf5e67bd312bed8de9e7b9744bf562dfac4c337d))

- Reject cleanup budget overflow
  ([`98c88b6`](https://github.com/n24q02m/better-drive/commit/98c88b6e85fbb7a13cd1668a58e27895065916b8))

- Reject duplicate inventory cursors ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Reject inventory byte count overflow
  ([`ae86974`](https://github.com/n24q02m/better-drive/commit/ae8697467e593f000b8e16a7c74ef6e462bcb811))

- Require authenticated backup evidence ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Require exact inventory metadata ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Restrict Scorecard to default branch ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Support macOS candidate verification ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- **ci**: Snapshot untagged candidate builds
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- **safety**: Harden local backup contracts
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

### Features

- Add authenticated artifact seal and open
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add cleanup approval workflow ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add config schema v2 and pinned runtime ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add credential-free release preparation ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add direction and replica transfer execution
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add exact-id cleanup controls ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add protected stable publication path via better-semantic-release
  ([#139](https://github.com/n24q02m/better-drive/pull/139),
  [`5bce437`](https://github.com/n24q02m/better-drive/commit/5bce43756293524f4768d87f1dc858c041a0d741))

- Add read-only managed scheduler surfaces
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add safe restore planning and staging ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Add transfer safety guard primitives ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Bind cleanup manifests to inventory ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Collect deterministic drive root sets ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Collect paginated drive inventory ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Complete local backup safety contracts ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Enforce enrolled cleanup approval roots ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Harden cleanup approval and inventory controls
  ([`8bae7f3`](https://github.com/n24q02m/better-drive/commit/8bae7f3484c26c5dc52f96b292546b30d0a8f177))

- Harden replica floors and runtime preflight
  ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Harden restore and installer gates ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Journal cleanup previews ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))

- Persist scheduler and replica state ([#134](https://github.com/n24q02m/better-drive/pull/134),
  [`edb394e`](https://github.com/n24q02m/better-drive/commit/edb394ed6c98aeb1e7c125b41a44ab1150e8b79c))


## v1.6.0 (2026-08-15)

### Bug Fixes

- Address scorecard security controls
  ([`7784896`](https://github.com/n24q02m/better-drive/commit/7784896cdabb265e5382b3d9e779328529ba3033))

- Document intentional error handling
  ([`f8e9d9d`](https://github.com/n24q02m/better-drive/commit/f8e9d9d83f56c0ad263bdf0154cf2886520f91fc))

- Handle resource close errors
  ([`6c10aa5`](https://github.com/n24q02m/better-drive/commit/6c10aa5d8bc6c785a24aaa103cbae82f386a267f))

- Harden rclone config path resolution
  ([`838039f`](https://github.com/n24q02m/better-drive/commit/838039f7d9b80b2233e03a686047a853a7d9568e))

- Harden systemd autostart path escaping
  ([`96d9a80`](https://github.com/n24q02m/better-drive/commit/96d9a801e4fb944b03d59bbf89bfde6beb932e6d))

- Harden tray path handling
  ([`e88cdaf`](https://github.com/n24q02m/better-drive/commit/e88cdafce86a37ba6f06651d0e305c26cb89bdcc))

- Move this repo to Apache-2.0 ([#63](https://github.com/n24q02m/better-drive/pull/63),
  [`213a74d`](https://github.com/n24q02m/better-drive/commit/213a74d34867d85f4aac67b4a2901bdcd7d5fd4f))

- Publish scorecard-recognized release signatures
  ([#98](https://github.com/n24q02m/better-drive/pull/98),
  [`59dd0e7`](https://github.com/n24q02m/better-drive/commit/59dd0e7ace70d23c89a37439ed68c7c49d20aaf3))

- Update non-major dependencies
  ([`f54f5c8`](https://github.com/n24q02m/better-drive/commit/f54f5c890461971826aff138ce728cdf44608808))

- **deps**: Update step-security/harden-runner action to v2.20.1
  ([#96](https://github.com/n24q02m/better-drive/pull/96),
  [`06abb54`](https://github.com/n24q02m/better-drive/commit/06abb54eb59b39dea07a6eaaaf76b4923d2967de))

### Features

- Add dynamic tray menu tooltips
  ([`451a21f`](https://github.com/n24q02m/better-drive/commit/451a21fa84444ec66262f0bf2710478cde5af98a))

- Add repository governance and developer guardrails
  ([`43cfb68`](https://github.com/n24q02m/better-drive/commit/43cfb68c5723697242f51bcc1e6b839c374d2892))

- Disable paused sync action
  ([`e432693`](https://github.com/n24q02m/better-drive/commit/e4326931048a12f15fe45944eb7dd695527f44b9))

- Finalize mount and sync reliability
  ([`6311b28`](https://github.com/n24q02m/better-drive/commit/6311b28569ff402c6fdef28730cea1a123bea366))

- Optimize ignore-line translation
  ([`59072a1`](https://github.com/n24q02m/better-drive/commit/59072a118774c63bbfb9e251e8cf0735104df86c))

- Sync cross-promo section ([#47](https://github.com/n24q02m/better-drive/pull/47),
  [`83e8ca7`](https://github.com/n24q02m/better-drive/commit/83e8ca7c43a8b2f5cf31efa869c4bf9dce9c0d4e))


## v1.5.1 (2026-07-25)

### Bug Fixes

- Publish the homebrew tap as a cask instead of the retired brews config
  ([`df7654c`](https://github.com/n24q02m/better-drive/commit/df7654c2ac491c027268e06a0a10621c0f79e20c))


## v1.5.0 (2026-07-25)


## v1.5.0-beta.4 (2026-07-25)

### Bug Fixes

- Document the sync --resync recovery path ([#40](https://github.com/n24q02m/better-drive/pull/40),
  [`8824071`](https://github.com/n24q02m/better-drive/commit/8824071cb60dfec449e0b0eca22d94400b1e12c3))

- Keep prereleases out of the scoop bucket and homebrew tap
  ([#38](https://github.com/n24q02m/better-drive/pull/38),
  [`a81ec55`](https://github.com/n24q02m/better-drive/commit/a81ec556952e857ad599a62036bf2f8749de4c77))

- Key each pair's bisync workdir by identity and add sync --resync
  ([#39](https://github.com/n24q02m/better-drive/pull/39),
  [`40bd257`](https://github.com/n24q02m/better-drive/commit/40bd2576e37fe5ed48ed65067d59d51756956cce))

- Key each pair's bisync workdir by identity instead of config position
  ([#39](https://github.com/n24q02m/better-drive/pull/39),
  [`40bd257`](https://github.com/n24q02m/better-drive/commit/40bd2576e37fe5ed48ed65067d59d51756956cce))

- Let the installer smoke test actually run and drop the dead branch trigger
  ([#37](https://github.com/n24q02m/better-drive/pull/37),
  [`4715bbf`](https://github.com/n24q02m/better-drive/commit/4715bbf8e673512f924b45fdb52f13e269a4a892))

- Pin ci.yml to the checkout and setup-go versions the other workflows use
  ([#32](https://github.com/n24q02m/better-drive/pull/32),
  [`e0ed2fa`](https://github.com/n24q02m/better-drive/commit/e0ed2fa33279e82cbd89b6304c0d1719773bb33f))

- Recognise the lost-baseline bisync abort under --resilient
  ([#39](https://github.com/n24q02m/better-drive/pull/39),
  [`40bd257`](https://github.com/n24q02m/better-drive/commit/40bd2576e37fe5ed48ed65067d59d51756956cce))

- Write the last two Vietnamese code comments in English
  ([#31](https://github.com/n24q02m/better-drive/pull/31),
  [`cfc3d00`](https://github.com/n24q02m/better-drive/commit/cfc3d0080b9c2891d14ff30390fb40c30da040fa))

- **deps**: Update actions/checkout action to v7
  ([#34](https://github.com/n24q02m/better-drive/pull/34),
  [`7cd1b75`](https://github.com/n24q02m/better-drive/commit/7cd1b759a9e1fe2ffe10be465f3e54e19beb00c1))

- **deps**: Update actions/setup-go action to v7
  ([#35](https://github.com/n24q02m/better-drive/pull/35),
  [`b86ed98`](https://github.com/n24q02m/better-drive/commit/b86ed98fb13b97b24c7d73f32d5e9aef167e9ee9))

- **deps**: Update github/codeql-action action to v4
  ([#36](https://github.com/n24q02m/better-drive/pull/36),
  [`dd261c9`](https://github.com/n24q02m/better-drive/commit/dd261c9467329249ab890a792b2ab3ecc3930e93))

- **deps**: Update go module directive to v1.26.5
  ([#33](https://github.com/n24q02m/better-drive/pull/33),
  [`ce21f6a`](https://github.com/n24q02m/better-drive/commit/ce21f6aed7ac9ecfb778374e400d651259def90f))

### Features

- Add sync --resync to rebuild a lost bisync baseline
  ([#39](https://github.com/n24q02m/better-drive/pull/39),
  [`40bd257`](https://github.com/n24q02m/better-drive/commit/40bd2576e37fe5ed48ed65067d59d51756956cce))


## v1.5.0-beta.3 (2026-07-25)

### Bug Fixes

- Name the way out in the guard messages, not only in the json remediation
  ([#29](https://github.com/n24q02m/better-drive/pull/29),
  [`4f2ce21`](https://github.com/n24q02m/better-drive/commit/4f2ce21ccd1679e795d1cf6c2369f104fa2b614e))

- Read .driveignore through os.OpenRoot so symlinks cannot escape the pair root
  ([`f916caf`](https://github.com/n24q02m/better-drive/commit/f916caf61a6c9c4309a73c7a7273b19dbd5c3795))

- Walk rclone output without materialising a line slice
  ([`c13936a`](https://github.com/n24q02m/better-drive/commit/c13936ae83d4fd793e91c052e4ab124899a47736))

### Features

- Add account list showing Drive remotes, their state and the pairs using them
  ([#29](https://github.com/n24q02m/better-drive/pull/29),
  [`4f2ce21`](https://github.com/n24q02m/better-drive/commit/4f2ce21ccd1679e795d1cf6c2369f104fa2b614e))

- Add account remove guarded against deleting a remote still in use
  ([#29](https://github.com/n24q02m/better-drive/pull/29),
  [`4f2ce21`](https://github.com/n24q02m/better-drive/commit/4f2ce21ccd1679e795d1cf6c2369f104fa2b614e))

- Add engine methods to list Drive remotes and read their quota
  ([#29](https://github.com/n24q02m/better-drive/pull/29),
  [`4f2ce21`](https://github.com/n24q02m/better-drive/commit/4f2ce21ccd1679e795d1cf6c2369f104fa2b614e))

- Add the account command group and non-interactive setup
  ([#29](https://github.com/n24q02m/better-drive/pull/29),
  [`4f2ce21`](https://github.com/n24q02m/better-drive/commit/4f2ce21ccd1679e795d1cf6c2369f104fa2b614e))

- Build the darwin release binaries with cgo so the tray works on macOS
  ([#28](https://github.com/n24q02m/better-drive/pull/28),
  [`616c9fb`](https://github.com/n24q02m/better-drive/commit/616c9fb6701a94f9cfde60edbafe8b7cfa164d5e))

- Document the account commands, non-interactive setup and macOS tray
  ([#30](https://github.com/n24q02m/better-drive/pull/30),
  [`0372b50`](https://github.com/n24q02m/better-drive/commit/0372b50fce3a0ea111ecb5a849fecf56a05c6ea2))

- Emit a JSON error envelope for --format json command failures
  ([#27](https://github.com/n24q02m/better-drive/pull/27),
  [`b7a1ed5`](https://github.com/n24q02m/better-drive/commit/b7a1ed58e25d7164f49dfdd42f6c4d4720694d72))

- Enable the system tray and cgo darwin release builds on macOS
  ([#28](https://github.com/n24q02m/better-drive/pull/28),
  [`616c9fb`](https://github.com/n24q02m/better-drive/commit/616c9fb6701a94f9cfde60edbafe8b7cfa164d5e))

- Enable the system tray on macOS ([#28](https://github.com/n24q02m/better-drive/pull/28),
  [`616c9fb`](https://github.com/n24q02m/better-drive/commit/616c9fb6701a94f9cfde60edbafe8b7cfa164d5e))

- Expose setup as account add without changing its output
  ([#29](https://github.com/n24q02m/better-drive/pull/29),
  [`4f2ce21`](https://github.com/n24q02m/better-drive/commit/4f2ce21ccd1679e795d1cf6c2369f104fa2b614e))

- Group tray menu actions with separators
  ([`b449a50`](https://github.com/n24q02m/better-drive/commit/b449a507ed18de4011ca9e8fc143c35846d57b62))

- Let setup run without a browser via token or service-account credentials
  ([#29](https://github.com/n24q02m/better-drive/pull/29),
  [`4f2ce21`](https://github.com/n24q02m/better-drive/commit/4f2ce21ccd1679e795d1cf6c2369f104fa2b614e))


## v1.5.0-beta.2 (2026-07-25)

### Bug Fixes

- Skip the real resync mkdir under --dry-run so no changes are made
  ([`23abbe1`](https://github.com/n24q02m/better-drive/commit/23abbe196e1e1489499b18458558e6964f56c565))

### Features

- Make the CLI agent-drivable (json output, exit codes, stderr hygiene, dry-run)
  ([`4117b11`](https://github.com/n24q02m/better-drive/commit/4117b11de37846a3b1872b2dc3cfa2b3937b4f93))


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
