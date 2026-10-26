# Axiom changes

Each item below is a statement that became true after the cutoff. Where a newer statement supersedes an older one, the older statement should no longer be relied on.

## go (version <= 1.27.3)

2 changed, 9 added, 0 restated; 58 axiom(s) were already true at the cutoff.

### Changed

- **[1.27.0]** Go 1.27 adds the `encoding/json/v2` and `encoding/json/jsontext` packages; `encoding/json/v2` uses stricter defaults that reject invalid UTF-8 and duplicate object names, while `encoding/json` is backed by the v2 implementation and retains its API.
  - Supersedes [1.25.0] Go 1.25 introduces experimental `encoding/json/v2` and `encoding/json/jsontext` packages, enabled with `GOEXPERIMENT=jsonv2`; with that experiment, `encoding/json` also uses the new implementation.
- **[1.27.0]** Go 1.27 adds an experimental portable `simd` package for vector-size-agnostic SIMD, enabled at build time with `GOEXPERIMENT=simd`; the experimental `simd/archsimd` package also adds arm64 and WebAssembly SIMD support.
  - Supersedes [1.26.0] Go 1.26 adds an experimental `simd/archsimd` package with architecture-specific SIMD operations on amd64, enabled by setting `GOEXPERIMENT=simd` at build time.

### Added

- **[1.27.0]** Go 1.27 supports generic methods with method-specific type parameters; for example, `func (s *Store) Get[T any](key string) T` declares a method type parameter.
- **[1.27.0]** Go 1.27 adds `crypto/mldsa` implementing FIPS 204 ML-DSA signatures and adds ML-DSA key and signature support to `crypto/x509` and ML-DSA signatures in TLS 1.3.
- **[1.27.0]** Go 1.27 adds the `uuid` package for generating and parsing UUIDs.
- **[1.27.0]** Go 1.27 adds `bytes.CutLast` and `strings.CutLast` to split a byte slice or string around the last occurrence of a separator.
- **[1.27.0]** Go 1.27 removes support for the Bazaar (bzr) version control system from the `go` command.
- **[1.27.0]** Go 1.27 adds generic `errors.AsType`, which provides a type-safe way to extract a value of a target type from an error chain.
- **[1.27.0]** Starting in Go 1.27, the `tlsunsafeekm`, `tlsrsakex`, `tls3des`, `tls10server`, and `x509keypairleaf` GODEBUG settings no longer restore legacy behavior; TLS keying-material export requires TLS 1.3 or Extended Master Secret, RSA-only key exchange and 3DES are not enabled by default, the default minimum TLS version is TLS 1.2, and X.509 key-pair loading populates `Certificate.Leaf`.
- **[1.27.0]** Go 1.27 adds `database/sql.ConvertAssign` and the `database/sql/driver.RowsColumnScanner` interface for database drivers to reuse `Rows.Scan` conversions or scan directly into supplied destinations.
- **[1.27.0]** Go 1.27 adds `hash/maphash.Hasher` and `ComparableHasher` for hashing values according to their equality relation.

## snapcraft (base <= 22)

3 changed, 8 added, 0 restated; 26 axiom(s) were already true at the cutoff.

### Changed

- **[22 9.1.0]** Snapcraft 9.1 removes the experimental Matter SDK plugin, so `parts:\n matter:\n plugin: matter-sdk\n source: .` is no longer a valid Snapcraft plugin declaration.
  - Supersedes [22 8.1.0] Snapcraft 8.1 adds the Matter SDK, Maven, and QMake plugins for core22, allowing parts such as `parts:\n app:\n plugin: maven\n source: .` to use those build systems.
- **[22 9.1.0]** Snapcraft 9.1 deprecates values of the project `license` key that are not valid SPDX license expressions; for example, use `license: MIT` rather than a non-SPDX value.
  - Supersedes [22 8.12.0] Snapcraft 8.12 adds a metadata linter that warns for missing or malformed `title`, `contact`, or `license` on stable-grade snaps and logs missing or malformed `donation`, `issues`, `source-code`, or `website` metadata.
- **[22 9.2.0]** Confdb views may declare a `parameters:` mapping, and storage paths can use field filters in the form `[.field={param}]`; the reference does not define a `filters:` mapping.
  - Supersedes [22 9.2.0] Snapcraft 9.2 supports parameters and filters for confdb schemas; parameters define named constraints used by filters to control which rules apply.

### Added

- **[22 9.1.0]** Snapcraft 9.1 provides a stable kernel plugin for building kernel snaps on supported bases; a project can declare `parts:\n kernel:\n plugin: kernel\n source: https://example.com/kernel.git`.
- **[22 9.1.0]** Snapcraft 9.1 adds an initrd plugin for building the initial ramdisk used by a kernel snap; a part can declare `parts:\n initrd:\n plugin: initrd`.
- **[22 9.1.0]** Snapcraft 9.1 adds per-component compression configuration, allowing a component to override the compression used for the snap; for example, `components:\n data:\n type: test\n compression: xz`.
- **[22 9.1.0]** Snapcraft 9.1 no longer supports Candid authentication for the Snap Store; authenticate with Ubuntu One instead.
- **[22 9.1.0]** On 64-bit ARM systems running a 32-bit userspace, Snapcraft 9.1 recognizes the `armv8l` machine type and maps it to ARMHF, allowing native ARMHF snap builds when `CRAFT_BUILD_ON=armhf` is set.
- **[22 9.2.0]** Snapcraft 9.2 accepts `name@version` notation for packages declared in `build-packages`, `stage-packages`, `build-snaps`, and `stage-snaps` (previous version notation remains supported through core26); for example, a part may use `stage-packages: [curl@8.5.0]`.
- **[22 9.2.0]** Snapcraft 9.2's `init` command accepts `--vcs` to initialize a Git repository when scaffolding a project; for example, run `snapcraft init --vcs git`.
- **[22 9.2.0]** Snapcraft 9.2 introduces the `stage-slices` part key for selecting Chisel slices to place in a part's install directory; for example, `parts:\n app:\n stage-slices: [ca-certificates_data]`.

## go:golang.org/x/sys (version <= 0.48.0)

1 changed, 3 added, 0 restated; 37 axiom(s) were already true at the cutoff.

### Changed

- **[0.48.0]** The module golang.org/x/sys requires Go 1.26.0 or newer beginning with v0.48.0; consumers must use a toolchain supporting Go 1.26.
  - Supersedes [0.31.0] The module golang.org/x/sys requires Go 1.23.0 or newer; Go modules selecting golang.org/x/sys v0.31.0 must therefore use a toolchain supporting that directive.
  - Supersedes [0.34.0] golang.org/x/sys v0.34.0 requires Go 1.23.0 or newer.
  - Supersedes [0.36.0] The module golang.org/x/sys requires Go 1.24.0 or newer beginning with v0.36.0; Go modules selecting this version require a toolchain that supports the Go 1.24 directive.
  - Supersedes [0.42.0] The module golang.org/x/sys requires Go 1.25.0 or newer starting with v0.42.0; consumers selecting this release need a Go 1.25-compatible toolchain.

### Added

- **[0.48.0]** The package golang.org/x/sys/unix provides Linux IPMI interface constants and types; for example, use unix.IPMICTL_SEND_COMMAND to identify the IPMI send-command ioctl.
- **[0.48.0]** The package golang.org/x/sys/unix provides Linux pidfd constants and the PidfdInfo type for working with pid file descriptors; for example, use unix.PIDFD_NONBLOCK when opening a pidfd.
- **[0.48.0]** The package golang.org/x/sys/windows exports SO_SNDTIMEO for configuring a Windows socket send timeout; for example, err := windows.SetsockoptInt(fd, windows.SOL_SOCKET, windows.SO_SNDTIMEO, milliseconds).

## go:golang.org/x/term (version <= 0.46.0)

1 changed, 1 added, 0 restated; 10 axiom(s) were already true at the cutoff.

### Changed

- **[0.46.0]** The golang.org/x/term module requires Go 1.26.0 or newer, as declared by its go.mod go directive.
  - Supersedes [0.30.0] The golang.org/x/term module requires Go 1.23.0 or newer, as declared by its go.mod go directive.
  - Supersedes [0.35.0] The golang.org/x/term module requires Go 1.24.0 or newer, as declared by its go.mod go directive.
  - Supersedes [0.41.0] The golang.org/x/term module requires Go 1.25.0 or newer, as declared by its go.mod go directive.

### Added

- **[0.46.0]** golang.org/x/term.Terminal.ReadLine processes bytes returned together with a non-nil reader error before reporting that error, preserving a complete buffered line for the current call and reporting the saved error on a subsequent call.
**end of axiom changes**
